package mesh

import (
	"net/netip"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// meshSubnet matches the control-plane's MESH_CIDR. Packets with destination
// in this prefix are mesh-bound (peer-to-peer through wg-go); everything
// else is fed to the non-mesh forwarder (tun2socks → SOCKS5 → xray exit).
var meshSubnet = netip.MustParsePrefix("10.100.0.0/16")

// splitter reads raw IP packets from the real OS TUN and dispatches them
// based on destination IP. It uses the wireguard-go tun.Device interface to
// read in batches — that path goes through Go's runtime netpoller for the
// underlying fd, so close-from-another-goroutine and non-blocking semantics
// work cleanly. The plain os.File-from-fd route blocks indefinitely on the
// Android TUN fd in practice.
type splitter struct {
	realDev tun.Device

	onMesh    func([]byte)
	onNonMesh func([]byte)

	stopped atomic.Bool
}

func (s *splitter) run() {
	batchSize := s.realDev.BatchSize()
	if batchSize < 1 {
		batchSize = 1
	}
	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, 65536)
	}
	sizes := make([]int, batchSize)

	for {
		if s.stopped.Load() {
			return
		}
		n, err := s.realDev.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			size := sizes[i]
			if size < 1 {
				continue
			}
			pkt := bufs[i][:size]
			dst, ok := parseDst(pkt)
			if !ok {
				continue
			}
			// Copy because bufs[i] is reused on the next Read.
			cp := make([]byte, size)
			copy(cp, pkt)

			if meshSubnet.Contains(dst) {
				if s.onMesh != nil {
					s.onMesh(cp)
				}
			} else {
				if s.onNonMesh != nil {
					s.onNonMesh(cp)
				}
			}
		}
	}
}

func (s *splitter) stop() {
	s.stopped.Store(true)
}

// parseDst pulls the destination address out of an IPv4 or IPv6 header.
// Returns (zero, false) for malformed packets so the caller can skip them
// without disrupting the read loop.
func parseDst(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 1 {
		return netip.Addr{}, false
	}
	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < 20 {
			return netip.Addr{}, false
		}
		var a [4]byte
		copy(a[:], pkt[16:20])
		return netip.AddrFrom4(a), true
	case 6:
		if len(pkt) < 40 {
			return netip.Addr{}, false
		}
		var a [16]byte
		copy(a[:], pkt[24:40])
		return netip.AddrFrom16(a), true
	default:
		return netip.Addr{}, false
	}
}
