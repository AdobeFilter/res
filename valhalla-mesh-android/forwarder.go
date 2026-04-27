package mesh

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"golang.org/x/net/proxy"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	fwdNICID    tcpip.NICID = 1
	fwdQueueLen             = 1024
)

// nonMeshForwarder is a tun2socks-style proxy: gvisor netstack on a virtual
// link endpoint terminates every TCP/UDP connection injected at it, and the
// per-flow handler proxies the L4 stream out through socksDialer (Kotlin
// xray's user-exit SOCKS5 inbound).
//
// The split is set up by the splitter: mesh-subnet packets go to wg-go,
// everything else lands here via inject(). Reply packets that gvisor
// generates come out the link endpoint's outbound queue and are written
// back to the OS TUN through writeBack.
//
// UDP is currently a no-op drop. TCP-only is enough for HTTP/HTTPS through
// the user-exit; modern apps fall back to DoH-over-TCP when UDP DNS fails,
// so most things still work. iter 2.1 will plumb UDP via SOCKS5 UDP
// ASSOCIATE.
type nonMeshForwarder struct {
	stack       *stack.Stack
	ep          *channel.Endpoint
	socksDialer proxy.ContextDialer
	writeBack   func([]byte) error
	closed      atomic.Bool
}

func newNonMeshForwarder(socksDialer proxy.ContextDialer, mtu int, writeBack func([]byte) error) (*nonMeshForwarder, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		HandleLocal:        false,
	})

	ep := channel.New(fwdQueueLen, uint32(mtu), "")
	if tcpipErr := s.CreateNIC(fwdNICID, ep); tcpipErr != nil {
		return nil, fmt.Errorf("CreateNIC: %v", tcpipErr)
	}
	// Promiscuous + spoofing: accept packets to ANY destination address
	// (since we forward arbitrary outbound traffic) and let the netstack
	// originate replies with arbitrary source addresses (the original dst,
	// pretending to be the destination).
	if tcpipErr := s.SetPromiscuousMode(fwdNICID, true); tcpipErr != nil {
		return nil, fmt.Errorf("SetPromiscuousMode: %v", tcpipErr)
	}
	if tcpipErr := s.SetSpoofing(fwdNICID, true); tcpipErr != nil {
		return nil, fmt.Errorf("SetSpoofing: %v", tcpipErr)
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: fwdNICID},
		{Destination: header.IPv6EmptySubnet, NIC: fwdNICID},
	})

	f := &nonMeshForwarder{
		stack:       s,
		ep:          ep,
		socksDialer: socksDialer,
		writeBack:   writeBack,
	}

	// 2048 max-in-flight half-open connections is plenty for a phone.
	tcpFwd := tcp.NewForwarder(s, 0, 2048, f.acceptTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	udpFwd := udp.NewForwarder(s, f.acceptUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	go f.outboundPump()
	return f, nil
}

func (f *nonMeshForwarder) inject(pkt []byte) {
	if f.closed.Load() || len(pkt) < 1 {
		return
	}
	var protoNumber tcpip.NetworkProtocolNumber
	switch pkt[0] >> 4 {
	case 4:
		protoNumber = ipv4.ProtocolNumber
	case 6:
		protoNumber = ipv6.ProtocolNumber
	default:
		return
	}
	pktBuf := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	f.ep.InjectInbound(protoNumber, pktBuf)
	pktBuf.DecRef()
}

// outboundPump drains gvisor's outbound queue (reply packets the netstack
// generated) onto the OS TUN. ReadContext returns nil once the channel
// endpoint is Closed, so we just terminate then.
func (f *nonMeshForwarder) outboundPump() {
	ctx := context.Background()
	for {
		pkt := f.ep.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		pkt.DecRef()
		if view == nil {
			continue
		}
		bytes := view.AsSlice()
		if err := f.writeBack(bytes); err != nil {
			return
		}
	}
}

func (f *nonMeshForwarder) close() {
	if f.closed.CompareAndSwap(false, true) {
		f.ep.Close()
		f.stack.Close()
	}
}

// acceptTCP runs once per incoming TCP SYN. We open a SOCKS5 connection to
// the original destination (pulled from the netstack's TransportEndpointID)
// and shuttle bytes between gvisor's socket and the SOCKS conn until either
// side closes.
func (f *nonMeshForwarder) acceptTCP(req *tcp.ForwarderRequest) {
	id := req.ID()
	var wq waiter.Queue
	netstackEP, errEP := req.CreateEndpoint(&wq)
	if errEP != nil {
		req.Complete(true)
		return
	}
	req.Complete(false)

	netstackConn := gonet.NewTCPConn(&wq, netstackEP)
	dstHost := net.IP(id.LocalAddress.AsSlice()).String()
	dstAddr := net.JoinHostPort(dstHost, fmt.Sprint(id.LocalPort))

	go func() {
		defer netstackConn.Close()
		outConn, err := f.socksDialer.DialContext(context.Background(), "tcp", dstAddr)
		if err != nil {
			return
		}
		defer outConn.Close()

		done := make(chan struct{}, 2)
		go func() {
			_, _ = io.Copy(outConn, netstackConn)
			outConn.Close()
			netstackConn.Close()
			done <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(netstackConn, outConn)
			outConn.Close()
			netstackConn.Close()
			done <- struct{}{}
		}()
		<-done
		<-done
	}()
}

// acceptUDP returns false to signal "not handled" — gvisor's default
// behavior is to drop the datagram. Real UDP forwarding (SOCKS5 UDP
// ASSOCIATE) is iter 2.1; for now apps that need UDP outside the mesh
// fail their UDP flows, but DoH-capable apps still resolve and connect
// via TCP.
func (f *nonMeshForwarder) acceptUDP(req *udp.ForwarderRequest) bool {
	_ = req
	return false
}
