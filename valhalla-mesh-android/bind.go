package mesh

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/conn"
)

// Frame protocol — must stay in sync with relay-node/mesh.
const (
	frameHello    byte = 0x01
	frameDatagram byte = 0x02
	pubkeyLen          = 32
	maxFrame           = 65535
)

// MeshBind is a wireguard-go conn.Bind that tunnels every WG UDP packet over
// a single TCP stream to the relay's mesh dispatcher (the dial closure
// produces that stream, typically by punching through a SOCKS5 proxy that
// fronts xray on the Kotlin side).
type MeshBind struct {
	dial       func() (net.Conn, error)
	selfPubkey [pubkeyLen]byte

	mu     sync.Mutex
	conn   net.Conn
	open   atomic.Bool
	closed atomic.Bool

	readCh chan meshPacket
}

type meshPacket struct {
	data []byte
	ep   *meshEndpoint
}

func NewMeshBind(selfPubkey [pubkeyLen]byte, dial func() (net.Conn, error)) *MeshBind {
	return &MeshBind{
		dial:       dial,
		selfPubkey: selfPubkey,
		readCh:     make(chan meshPacket, 256),
	}
}

func (b *MeshBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open.Load() {
		return nil, 0, errors.New("mesh bind already open")
	}
	c, err := b.dial()
	if err != nil {
		return nil, 0, fmt.Errorf("dial relay: %w", err)
	}
	if err := writeFrame(c, frameHello, b.selfPubkey[:]); err != nil {
		c.Close()
		return nil, 0, fmt.Errorf("hello: %w", err)
	}
	b.conn = c
	b.open.Store(true)
	b.closed.Store(false)

	go b.readLoop(c)

	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		return b.receive(packets, sizes, eps)
	}
	return []conn.ReceiveFunc{recv}, port, nil
}

func (b *MeshBind) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.open.Store(false)
	b.mu.Lock()
	c := b.conn
	b.conn = nil
	b.mu.Unlock()
	// Same close-race avoidance as the Linux client: do NOT close readCh.
	if c != nil {
		_ = c.Close()
	}
	return nil
}

func (b *MeshBind) SetMark(mark uint32) error { return nil }
func (b *MeshBind) BatchSize() int             { return 1 }

func (b *MeshBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return parseMeshEndpoint(s)
}

func (b *MeshBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	me, ok := ep.(*meshEndpoint)
	if !ok {
		return fmt.Errorf("mesh bind: unexpected endpoint type %T", ep)
	}
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		return errors.New("mesh bind not open")
	}
	for _, buf := range bufs {
		payload := make([]byte, pubkeyLen+len(buf))
		copy(payload[:pubkeyLen], me.pubkey[:])
		copy(payload[pubkeyLen:], buf)
		if err := writeFrame(c, frameDatagram, payload); err != nil {
			return err
		}
	}
	return nil
}

func (b *MeshBind) readLoop(c net.Conn) {
	for {
		ft, payload, err := readFrame(c)
		if err != nil {
			return
		}
		if ft != frameDatagram || len(payload) < pubkeyLen+1 {
			continue
		}
		var srcPK [pubkeyLen]byte
		copy(srcPK[:], payload[:pubkeyLen])
		data := make([]byte, len(payload)-pubkeyLen)
		copy(data, payload[pubkeyLen:])
		select {
		case b.readCh <- meshPacket{data: data, ep: &meshEndpoint{pubkey: srcPK}}:
		default:
			// dropped — WG retransmits
		}
	}
}

func (b *MeshBind) receive(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	pkt, ok := <-b.readCh
	if !ok {
		return 0, net.ErrClosed
	}
	if b.closed.Load() {
		return 0, net.ErrClosed
	}
	if len(packets) == 0 || len(packets[0]) < len(pkt.data) {
		return 0, io.ErrShortBuffer
	}
	copy(packets[0], pkt.data)
	sizes[0] = len(pkt.data)
	eps[0] = pkt.ep
	return 1, nil
}

func writeFrame(w io.Writer, ft byte, payload []byte) error {
	if len(payload)+1 > maxFrame {
		return fmt.Errorf("frame too large: %d", len(payload)+1)
	}
	var hdr [3]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(1+len(payload)))
	hdr[2] = ft
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n < 1 || n > maxFrame {
		return 0, nil, fmt.Errorf("frame length out of range: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}
