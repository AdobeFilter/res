package mesh

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

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
	protector   SocketProtector
	writeBack   func([]byte) error
	closed      atomic.Bool
}

func newNonMeshForwarder(socksDialer proxy.ContextDialer, protector SocketProtector, mtu int, writeBack func([]byte) error) (*nonMeshForwarder, error) {
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
		protector:   protector,
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
		log.Printf("nonmesh tcp: CreateEndpoint failed: %v", errEP)
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
			log.Printf("nonmesh tcp: SOCKS5 dial %s failed: %v", dstAddr, err)
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

// acceptUDP handles each UDP "connection" gvisor extracts. DNS (port 53)
// is forwarded as DIRECT UDP via a kernel-protected socket — bypasses our
// own TUN at the kernel layer (VpnService.protect) and goes straight out
// the device's underlying network. This sidesteps user-exit servers that
// block outbound port 53 (a common VPS DNS-spam mitigation), which the
// previous DNS-over-TCP-via-SOCKS5 approach hit.
//
// Other UDP (QUIC, NTP, mDNS, …) is dropped — full proxying via the
// user-exit needs UDP-over-VLESS support that Vision-flow xray outbounds
// don't have. iter 2.2 may add a separate freedom-outbound path for them.
func (f *nonMeshForwarder) acceptUDP(req *udp.ForwarderRequest) bool {
	id := req.ID()
	if id.LocalPort != 53 {
		return false
	}
	if f.protector == nil {
		log.Printf("nonmesh udp53: drop — no protector wired up")
		return false
	}
	dstIP := net.IP(id.LocalAddress.AsSlice())

	var wq waiter.Queue
	netstackEP, errEP := req.CreateEndpoint(&wq)
	if errEP != nil {
		log.Printf("nonmesh udp53: CreateEndpoint failed: %v", errEP)
		return true
	}
	netstackConn := gonet.NewUDPConn(&wq, netstackEP)

	go f.proxyDNSDirect(netstackConn, dstIP)
	return true
}

// proxyDNSDirect ferries each UDP datagram between the netstack and a
// real protected UDP socket dialed straight at the DNS server's IP. The
// socket bypasses the VpnService TUN entirely thanks to
// VpnService.protect(fd) called via the SocketProtector before connect.
func (f *nonMeshForwarder) proxyDNSDirect(nsConn net.Conn, dstIP net.IP) {
	defer nsConn.Close()

	udpConn, err := f.openProtectedUDP4()
	if err != nil {
		log.Printf("nonmesh udp53: open protected udp failed: %v", err)
		return
	}
	defer udpConn.Close()

	dst := &net.UDPAddr{IP: dstIP, Port: 53}

	// Reader: pull replies off the protected socket, push them at gvisor.
	go func() {
		buf := make([]byte, 4096)
		for {
			_ = udpConn.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, _, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				_ = nsConn.Close()
				return
			}
			if _, err := nsConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// Writer: pull queries from gvisor, send them out the protected socket.
	buf := make([]byte, 4096)
	for {
		n, err := nsConn.Read(buf)
		if err != nil {
			return
		}
		if _, err := udpConn.WriteToUDP(buf[:n], dst); err != nil {
			log.Printf("nonmesh udp53: write %s failed: %v", dst, err)
			return
		}
	}
}

// openProtectedUDP4 opens a UDP4 socket on an ephemeral port, calls the
// Android side's VpnService.protect on its fd, and returns the conn.
// Without protect, the socket would be subject to VPN routing and form
// a packet loop with our own splitter.
func (f *nonMeshForwarder) openProtectedUDP4() (*net.UDPConn, error) {
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		uc.Close()
		return nil, err
	}
	var protectErr error
	ctlErr := rc.Control(func(fd uintptr) {
		if !f.protector.Protect(int(fd)) {
			protectErr = fmt.Errorf("VpnService.protect(%d) returned false", fd)
		}
	})
	if ctlErr != nil {
		uc.Close()
		return nil, ctlErr
	}
	if protectErr != nil {
		uc.Close()
		return nil, protectErr
	}
	return uc, nil
}
