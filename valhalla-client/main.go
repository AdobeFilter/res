// valhalla-client is a minimal Linux CLI that proves the L3-mesh path:
// userspace wireguard-go over a custom conn.Bind whose wire bytes go out
// through xray's VLESS+Reality outbound to the Valhalla relay's mesh
// dispatcher, which routes them to the destination peer by WG pubkey.
//
// The process runs in one of two modes:
//
//	-mode server  — opens a TCP echo listener on its mesh IP inside the
//	                netstack. Stays up until SIGINT.
//	-mode client  — dials the target peer's mesh IP on the same port and
//	                exchanges a handshake message. Prints RTT. Exits.
//
// The server endpoint uses the gVisor netstack TCP stack wrapped around
// the wireguard-go TUN, so there's no real TUN/kernel configuration — the
// test works on any Linux userland, including rootless VMs.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"valhalla/client/tunnel"
	"valhalla/common/protocol"
)

const (
	wgMTU            = 1280 // leaves headroom for VLESS+Reality overhead
	testEchoPort     = 9000
	handshakeMsg     = "valhalla-mesh-handshake\n"
	xrayDokodemoAddr = "127.0.0.1:10800"
	// meshDstAddr is the logical CONNECT target the xray outbound advertises.
	// The relay's routing rules only accept this exact address — anything
	// else gets blackholed, which is the point.
	meshDstAddr = "127.0.0.1:9999"
)

func main() {
	var (
		mode        = flag.String("mode", "", "server|client")
		controlURL  = flag.String("control", env("VALHALLA_CONTROL", "http://localhost:8443"), "control plane base URL")
		token       = flag.String("token", env("VALHALLA_TOKEN", ""), "account auth token")
		selfNodeID  = flag.String("self", env("VALHALLA_SELF_NODE", ""), "this device's node ID (registered via exit-node or API)")
		targetNode  = flag.String("target", env("VALHALLA_TARGET_NODE", ""), "peer node ID (client mode only)")
		wgPrivKeyB64 = flag.String("wg-key", env("VALHALLA_WG_KEY", ""), "base64 WG private key (matches the one registered for -self)")
		selfIP       = flag.String("self-ip", env("VALHALLA_SELF_IP", ""), "mesh IP assigned to this node by control plane")
		xrayBin      = flag.String("xray", env("VALHALLA_XRAY", "xray"), "xray binary path")
		exitLink     = flag.String("exit-link", env("VALHALLA_EXIT_LINK", ""), "VLESS link to user's exit-node (vless://...). When set, relay traffic is chained through it via xray proxySettings — needed when the relay's IP is DPI-blocked.")
	)
	flag.Parse()

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	if *mode != "server" && *mode != "client" {
		logger.Fatal("pass -mode server or -mode client")
	}
	if *token == "" || *selfNodeID == "" || *wgPrivKeyB64 == "" || *selfIP == "" {
		logger.Fatal("required flags: -token, -self, -wg-key, -self-ip (env VALHALLA_*)")
	}
	if *targetNode == "" {
		logger.Fatal("-target is required (the OTHER peer's node ID — same value on both VMs but mirrored)")
	}

	ctx, cancel := signalContext()
	defer cancel()

	// Both modes look up the route to the OTHER peer. Self-to-self has length-1
	// path which Dijkstra reports as ConnectionDirect, so the control-plane
	// never attaches relay credentials — fatal for the mesh MVP. WG also needs
	// the peer's pubkey on both ends to handshake, so server-mode without a
	// real -target couldn't construct an IpcSet anyway.
	resp, err := fetchRoute(ctx, *controlURL, *token, *selfNodeID, *targetNode)
	if err != nil {
		logger.Fatal("route lookup failed", zap.Error(err))
	}
	if resp.Relay == nil {
		logger.Fatal("control plane did not return relay credentials — connectivity must be ConnectionRelay for the mesh MVP")
	}
	logger.Info("route resolved",
		zap.String("conn_type", string(resp.ConnectionType)),
		zap.String("relay", fmt.Sprintf("%s:%d", resp.Relay.Address, resp.Relay.VLESSPort)),
		zap.String("dst_peer_ip", resp.DstPeer.InternalIP))

	// Optionally chain relay traffic through the user's exit-node when the
	// relay's IP is DPI-blocked. Parsed once up-front so a malformed link
	// fails before we go through xray spawn.
	var exit *tunnel.ExitNode
	if *exitLink != "" {
		exit, err = tunnel.ParseVLESSURL(*exitLink)
		if err != nil {
			logger.Fatal("parse -exit-link failed", zap.Error(err))
		}
		logger.Info("relay traffic chained through user exit-node",
			zap.String("exit_host", exit.Address),
			zap.Int("exit_port", exit.Port),
			zap.String("exit_sni", exit.SNI))
	}

	// Start xray subprocess: dokodemo on 127.0.0.1:10800 → VLESS+Reality → relay
	// (optionally proxied through user-exit when exit != nil).
	xc := tunnel.NewXrayClient(*xrayBin, xrayDokodemoAddr, meshDstAddr, resp.Relay, exit, logger)
	if err := xc.Start(ctx); err != nil {
		logger.Fatal("xray start failed", zap.Error(err))
	}
	defer xc.Stop()

	// Derive self pubkey bytes (for HELLO) from the base64 private key.
	privBytes, err := base64.StdEncoding.DecodeString(*wgPrivKeyB64)
	if err != nil || len(privBytes) != 32 {
		logger.Fatal("bad -wg-key", zap.Error(err))
	}
	var selfPubkey [32]byte
	copy(selfPubkey[:], derivePub(privBytes))

	// Custom bind: every WG UDP packet goes out as a frame on a TCP conn
	// to the xray dokodemo inbound (which wraps it in VLESS+Reality).
	bind := tunnel.NewMeshBind(logger, selfPubkey, func() (net.Conn, error) {
		return net.DialTimeout("tcp", xrayDokodemoAddr, 3*time.Second)
	})

	// Userspace WG stack with an in-memory TUN. We pick a /32 host route
	// for this node so anything addressed at selfIP goes through netstack.
	selfAddr, err := netip.ParseAddr(*selfIP)
	if err != nil {
		logger.Fatal("bad -self-ip", zap.Error(err))
	}
	tunDev, netstk, err := netstack.CreateNetTUN(
		[]netip.Addr{selfAddr},
		[]netip.Addr{netip.MustParseAddr("1.1.1.1")}, // dummy DNS, unused
		wgMTU,
	)
	if err != nil {
		logger.Fatal("netstack TUN create failed", zap.Error(err))
	}

	dev := device.NewDevice(tunDev, bind, device.NewLogger(device.LogLevelError, "[wg] "))

	// IpcSet config: self key, one peer (the routing target), endpoint is
	// our mesh-endpoint form ("vmesh:HEX"), so wireguard-go sends all this
	// peer's packets via our Bind with the right pubkey on the frame.
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(privBytes))
	if *mode == "server" {
		// Server has no outbound peer by default. It just listens and
		// reflects whatever shows up. That requires an allowed_ips rule
		// accepting all peers — we wildcard with 0.0.0.0/0 since the
		// mesh dispatcher only forwards frames from registered sessions
		// anyway.
		//
		// But wireguard-go still needs at least one [peer] block to
		// derive session state. We add the caller as a peer lazily when
		// their first handshake arrives... Actually WG doesn't support
		// that; it requires static peer configuration. So in server
		// mode, the operator must pre-provision the client's pubkey via
		// -peer-pubkey. For the MVP we keep it simple: the peer block
		// mirrors the one in client mode — you pass -target pointing at
		// the OTHER side, same as client. Control plane fills DstPeer.
		fmt.Fprintf(&sb, "listen_port=0\n")
		fmt.Fprintf(&sb, "public_key=%s\n", base64ToHex(resp.DstPeer.PublicKey))
		fmt.Fprintf(&sb, "endpoint=vmesh:%s\n", base64ToHex(resp.DstPeer.PublicKey))
		fmt.Fprintf(&sb, "allowed_ip=%s/32\n", resp.DstPeer.InternalIP)
		fmt.Fprintf(&sb, "persistent_keepalive_interval=25\n")
	} else {
		fmt.Fprintf(&sb, "listen_port=0\n")
		fmt.Fprintf(&sb, "public_key=%s\n", base64ToHex(resp.DstPeer.PublicKey))
		fmt.Fprintf(&sb, "endpoint=vmesh:%s\n", base64ToHex(resp.DstPeer.PublicKey))
		fmt.Fprintf(&sb, "allowed_ip=%s/32\n", resp.DstPeer.InternalIP)
		fmt.Fprintf(&sb, "persistent_keepalive_interval=25\n")
	}
	if err := dev.IpcSet(sb.String()); err != nil {
		logger.Fatal("wireguard IpcSet failed", zap.Error(err))
	}
	if err := dev.Up(); err != nil {
		logger.Fatal("wireguard Up failed", zap.Error(err))
	}
	defer dev.Close()

	logger.Info("mesh up", zap.String("self_ip", *selfIP), zap.String("mode", *mode))

	switch *mode {
	case "server":
		runServer(ctx, logger, netstk, selfAddr)
	case "client":
		runClient(ctx, logger, netstk, resp.DstPeer.InternalIP)
	}
}

func runServer(ctx context.Context, logger *zap.Logger, n *netstack.Net, selfAddr netip.Addr) {
	ln, err := n.ListenTCP(&net.TCPAddr{IP: selfAddr.AsSlice(), Port: testEchoPort})
	if err != nil {
		logger.Fatal("listen failed", zap.Error(err))
	}
	logger.Info("echo server listening", zap.String("addr", fmt.Sprintf("%s:%d", selfAddr, testEchoPort)))

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}(c)
	}
}

func runClient(ctx context.Context, logger *zap.Logger, n *netstack.Net, targetIP string) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// WG handshake takes a couple of round trips to settle; we poll.
	deadline := time.Now().Add(20 * time.Second)
	var c net.Conn
	var err error
	for time.Now().Before(deadline) {
		c, err = n.Dial("tcp", fmt.Sprintf("%s:%d", targetIP, testEchoPort))
		if err == nil {
			break
		}
		select {
		case <-dialCtx.Done():
			logger.Fatal("dial timeout", zap.Error(err))
		case <-time.After(500 * time.Millisecond):
		}
	}
	if c == nil {
		logger.Fatal("dial failed", zap.Error(err))
	}
	defer c.Close()

	start := time.Now()
	if _, err := c.Write([]byte(handshakeMsg)); err != nil {
		logger.Fatal("write failed", zap.Error(err))
	}
	reply := make([]byte, len(handshakeMsg))
	if _, err := io.ReadFull(c, reply); err != nil {
		logger.Fatal("read failed", zap.Error(err))
	}
	rtt := time.Since(start)
	if string(reply) != handshakeMsg {
		logger.Fatal("echo mismatch", zap.String("got", string(reply)))
	}
	fmt.Printf("ok — mesh echo rtt=%s\n", rtt)
}

// --- helpers ---

func fetchRoute(ctx context.Context, base, token, from, to string) (*protocol.RouteResponse, error) {
	u := fmt.Sprintf("%s/api/v1/routes/optimal?from=%s&to=%s", base, from, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("control plane %d: %s", resp.StatusCode, string(body))
	}
	var rr protocol.RouteResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, err
	}
	return &rr, nil
}

func derivePub(priv []byte) []byte {
	// We duplicate this little bit of crypto rather than pull in the
	// whole valhalla/common/crypto helper: that package re-exports a
	// string-typed API (base64 in/out), whereas here we already have
	// raw bytes and want raw bytes back.
	var in, out [32]byte
	copy(in[:], priv)
	// clamp
	in[0] &= 248
	in[31] &= 127
	in[31] |= 64
	curve25519ScalarBaseMult(&out, &in)
	return out[:]
}

// Implemented in curve25519_stub.go to keep the import surface of main.go
// narrow (avoids pulling golang.org/x/crypto into every TU that reads
// this file in review).
//
// Do not inline — see that file for the actual implementation.
var curve25519ScalarBaseMult func(dst, src *[32]byte) = nil

func base64ToHex(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Upstream is the control plane; a bad value here is a bug
		// elsewhere. Panic is appropriate — we'd corrupt the wg config
		// otherwise.
		panic(fmt.Sprintf("bad base64 pubkey %q: %v", b64, err))
	}
	return hex.EncodeToString(raw)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
