package mesh

import (
	"io"
	"net/netip"
	"sync/atomic"
)

// meshSubnet matches the control-plane's MESH_CIDR. Packets with destination
// in this prefix are mesh-bound (peer-to-peer through wg-go); everything
// else is fed to the non-mesh forwarder (tun2socks → SOCKS5 → xray exit).
var meshSubnet = netip.MustParsePrefix("10.100.0.0/16")

// splitter reads raw IP packets from the real OS TUN and dispatches them
// based on destination IP. Both consumers (wg-go via virtualTUN, the non-
// mesh forwarder) eventually write reply packets back to the real TUN via
// their own writeBack callbacks set up by the caller.
//
// The splitter copies each packet because the buffer it reads into is
// reused on the next Read; sending the same slice across a channel without
// copying would cause data races as soon as the next Read overwrites.
type splitter struct {
	realTun io.Reader

	onMesh    func([]byte)
	onNonMesh func([]byte)

	stopped atomic.Bool
}

func (s *splitter) run() {
	buf := make([]byte, 65536)
	for {
		if s.stopped.Load() {
			return
		}
		n, err := s.realTun.Read(buf)
		if err != nil {
			return
		}
		if n < 1 {
			continue
		}
		dst, ok := parseDst(buf[:n])
		if !ok {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		if meshSubnet.Contains(dst) {
			if s.onMesh != nil {
				s.onMesh(pkt)
			}
		} else {
			if s.onNonMesh != nil {
				s.onNonMesh(pkt)
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
