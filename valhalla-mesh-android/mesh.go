// Package mesh is the Android-facing entry point for the Valhalla L3 mesh.
// gomobile binds these symbols into a Java/Kotlin AAR.
//
// Architecture (mirrors valhalla-client/Linux):
//
//	WG userspace (this AAR) ──frame proto──→ TCP-conn ──┐
//	                                                    │ via SOCKS5 from
//	                                                    │ Kotlin-managed xray
//	                                                    ▼
//	                                  outer VLESS+Reality ──→ user exit-node
//	                                                    ──→ relay :8444 plain VLESS
//	                                                    ──→ mesh dispatcher (forward by pubkey)
//
// Kotlin responsibilities:
//   - acquire VPN permission, build TUN via VpnService.Builder, detach FD
//   - GET /api/v1/routes/optimal → relay endpoint + peer pubkey/IP
//   - configure xray with SOCKS5 inbound + chained VLESS outbounds
//     (relay → user-exit) so this AAR's SOCKS5 dial reaches the
//     relay's mesh dispatcher through the user's exit-node
//   - call StartMesh(...) with the FD, the peer info, and the SOCKS5 address
//
// Go responsibilities (this AAR):
//   - SOCKS5-dial 127.0.0.1:9999 once, send HELLO, then run the mesh-frame
//     protocol over that single TCP stream
//   - run wireguard-go on the TUN FD with a custom Bind that funnels every
//     WG packet onto that stream
package mesh

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/net/proxy"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// Session is the running mesh tunnel. Keep a reference in Kotlin to call Stop.
type Session struct {
	dev *device.Device
}

// Stop tears the tunnel down. Idempotent.
func (s *Session) Stop() {
	if s.dev != nil {
		s.dev.Close()
		s.dev = nil
	}
}

// Hello is a smoke test — call from Kotlin to confirm the AAR loaded.
func Hello(name string) string {
	return fmt.Sprintf("hello from valhalla-mesh, %s", name)
}

// StartMesh starts the mesh tunnel. Kotlin is responsible for fetching the
// route from the control-plane and configuring the xray chain — by the time
// this is called, the SOCKS5 inbound at socksAddr already routes every
// connection through the relay+user-exit chain.
//
// Args:
//
//	tunFD                file descriptor from VpnService.Builder.establish().detachFd()
//	targetPeerPubKeyB64  base64 WG pubkey of the peer (RouteResponse.dst_peer.public_key)
//	targetPeerInternalIP mesh IP of the peer (RouteResponse.dst_peer.internal_ip)
//	wgPrivKeyB64         base64 WG private key (matches our registered pubkey)
//	selfIP               mesh IP assigned to this node
//	socksAddr            SOCKS5 proxy "host:port" — Kotlin xray's mesh-chain inbound
func StartMesh(
	tunFD int32,
	targetPeerPubKeyB64 string,
	targetPeerInternalIP string,
	wgPrivKeyB64 string,
	selfIP string,
	socksAddr string,
) (*Session, error) {
	if socksAddr == "" {
		return nil, fmt.Errorf("socksAddr is required (point at Kotlin xray's SOCKS5 inbound)")
	}
	socksDialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("socks5 dialer: %w", err)
	}
	ctxDialer, ok := socksDialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5 dialer is not ContextDialer")
	}

	// SOCKS5 dial target is the mesh-dispatcher advertised address — the
	// xray chain on the Kotlin side carries this destination through
	// user-exit to the relay's vless-plain-in inbound, which routes by
	// inboundTag + (127.0.0.1, 9999) to the mesh dispatcher.
	const meshDstAddr = "127.0.0.1:9999"

	tunDev, _, err := tun.CreateUnmonitoredTUNFromFD(int(tunFD))
	if err != nil {
		return nil, fmt.Errorf("tun from fd: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(wgPrivKeyB64)
	if err != nil || len(privBytes) != 32 {
		return nil, fmt.Errorf("bad wg-key: must be base64 of 32 bytes")
	}
	var selfPub [pubkeyLen]byte
	derivePub(&selfPub, privBytes)

	bind := NewMeshBind(selfPub, func() (net.Conn, error) {
		return ctxDialer.DialContext(context.Background(), "tcp", meshDstAddr)
	})

	dev := device.NewDevice(tunDev, bind, &device.Logger{
		Verbosef: func(format string, args ...any) {},
		Errorf:   func(format string, args ...any) {},
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(privBytes))
	fmt.Fprintf(&sb, "public_key=%s\n", base64ToHex(targetPeerPubKeyB64))
	fmt.Fprintf(&sb, "endpoint=vmesh:%s\n", base64ToHex(targetPeerPubKeyB64))
	fmt.Fprintf(&sb, "allowed_ip=%s/32\n", targetPeerInternalIP)
	fmt.Fprintf(&sb, "persistent_keepalive_interval=25\n")

	if err := dev.IpcSet(sb.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg ipc: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg up: %w", err)
	}

	_ = selfIP // reserved for diagnostics; the IP is already on the TUN via VpnService config
	return &Session{dev: dev}, nil
}

// --- helpers ---

func derivePub(out *[32]byte, priv []byte) {
	var in [32]byte
	copy(in[:], priv)
	in[0] &= 248
	in[31] &= 127
	in[31] |= 64
	curve25519.ScalarBaseMult(out, &in)
}

func base64ToHex(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Upstream is the control plane; a bad value here is a bug elsewhere.
		// We can't return errors from this hot path, so panic — Kotlin sees
		// it as a crash and we fix the source.
		panic(fmt.Sprintf("bad base64 pubkey %q: %v", b64, err))
	}
	return hex.EncodeToString(raw)
}
