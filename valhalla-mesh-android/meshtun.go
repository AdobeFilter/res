package mesh

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// virtualTUN is a tun.Device backed by a Go channel + a write callback
// instead of a real OS interface. It exists so wg-go can run on top of a
// stream of IP packets we choose to feed it (mesh subnet only) while the
// real OS TUN is owned by the splitter, which fans packets out to either
// wg-go or the non-mesh path.
//
// Read pulls a packet the splitter pushed onto incoming. Write hands the
// packet wg-go just decrypted to writeBack, which forwards it to the real
// OS TUN so the app sees the response.
type virtualTUN struct {
	incoming  chan []byte
	writeBack func([]byte) error

	mtu  int
	name string

	events chan tun.Event
	closed atomic.Bool
	once   sync.Once
}

func newVirtualTUN(name string, mtu int, writeBack func([]byte) error) *virtualTUN {
	return &virtualTUN{
		incoming:  make(chan []byte, 256),
		writeBack: writeBack,
		mtu:       mtu,
		name:      name,
		events:    make(chan tun.Event, 4),
	}
}

func (v *virtualTUN) Read(buffs [][]byte, sizes []int, offset int) (int, error) {
	if v.closed.Load() {
		return 0, os.ErrClosed
	}
	pkt, ok := <-v.incoming
	if !ok {
		return 0, os.ErrClosed
	}
	if len(buffs) == 0 || len(sizes) == 0 {
		return 0, errors.New("virtualTUN.Read: empty buffs")
	}
	if len(buffs[0])-offset < len(pkt) {
		return 0, errors.New("virtualTUN.Read: buffer too small")
	}
	copy(buffs[0][offset:], pkt)
	sizes[0] = len(pkt)
	return 1, nil
}

func (v *virtualTUN) Write(buffs [][]byte, offset int) (int, error) {
	if v.closed.Load() {
		return 0, os.ErrClosed
	}
	n := 0
	for _, b := range buffs {
		if len(b) <= offset {
			continue
		}
		if err := v.writeBack(b[offset:]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (v *virtualTUN) MTU() (int, error)        { return v.mtu, nil }
func (v *virtualTUN) Name() (string, error)    { return v.name, nil }
func (v *virtualTUN) Events() <-chan tun.Event { return v.events }
func (v *virtualTUN) BatchSize() int           { return 1 }
func (v *virtualTUN) File() *os.File           { return nil }

func (v *virtualTUN) Close() error {
	v.once.Do(func() {
		v.closed.Store(true)
		// Don't close(v.incoming): the splitter might still try to push,
		// closed channel + send would panic. Drop-on-send pattern in the
		// caller plus closed flag here is enough — readers see ErrClosed.
		select {
		case v.events <- tun.EventDown:
		default:
		}
		close(v.events)
	})
	return nil
}

// push forwards a packet from the splitter into the channel wg-go reads
// from. Returns false if the channel is full — the splitter then drops the
// packet, which wg-go retransmits via WG anyway.
func (v *virtualTUN) push(pkt []byte) bool {
	if v.closed.Load() {
		return false
	}
	select {
	case v.incoming <- pkt:
		return true
	default:
		return false
	}
}
