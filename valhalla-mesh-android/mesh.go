// Package mesh is the Android-facing entry point for the Valhalla L3 mesh.
// gomobile binds these symbols into a Java/Kotlin AAR.
//
// Architecture (combined service that handles BOTH peer-mesh and full-tunnel
// exit-node internet through one VpnService — Android allows only one VPN at
// a time, so the two paths must coexist on a single TUN):
//
//	OS TUN (10.100.0.0/16 + 0.0.0.0/0)
//	   ↓ raw IP packets
//	splitter (this package)
//	   ├─ dst in 10.100.0.0/16 → virtualTUN ─→ wg-go ─→ MeshBind ─→ SOCKS5
//	   │                                                    Kotlin xray
//	   │                                                    (relay-chain
//	   │                                                     through user-exit)
//	   │                                                    ─→ relay :8444
//	   │                                                    ─→ mesh dispatcher
//	   └─ everything else → tun2socks ─→ SOCKS5 ─→ Kotlin xray ─→ user-exit
//
// Iteration 1 (this revision) plumbs the splitter + virtualTUN in front of
// wg-go without touching the non-mesh path — non-mesh packets are dropped
// for now. The Kotlin TUN still uses route 10.100.0.0/16 only, so nothing
// non-mesh ever reaches us. Subsequent iterations add tun2socks (full
// tunnel) and direct/relay auto-fallback for WG.
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

const wgMTU = 1280

// Session is the running mesh tunnel. Keep a reference in Kotlin to call Stop.
type Session struct {
	dev       *device.Device
	meshTun   *virtualTUN
	realDev   tun.Device
	splitter  *splitter
	forwarder *nonMeshForwarder
}

// AddPeer registers a mesh peer on the running wg-go device. Idempotent for
// the same pubkey — wg-go's UAPI updates the existing entry. Each peer is
// pinned to its mesh IP only (allowed_ip = single /32) so the splitter's
// per-packet routing matches.
//
// targetPeerPubKeyB64  base64 WG pubkey of the peer (RouteResponse.dst_peer.public_key)
// targetPeerInternalIP mesh IP of the peer (e.g. "10.100.0.20")
func (s *Session) AddPeer(targetPeerPubKeyB64 string, targetPeerInternalIP string) error {
	if s.dev == nil {
		return fmt.Errorf("AddPeer: session stopped")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "public_key=%s\n", base64ToHex(targetPeerPubKeyB64))
	fmt.Fprintf(&sb, "endpoint=vmesh:%s\n", base64ToHex(targetPeerPubKeyB64))
	fmt.Fprintf(&sb, "allowed_ip=%s/32\n", targetPeerInternalIP)
	fmt.Fprintf(&sb, "persistent_keepalive_interval=25\n")
	return s.dev.IpcSet(sb.String())
}

// Stop tears the tunnel down. Idempotent.
func (s *Session) Stop() {
	if s.splitter != nil {
		s.splitter.stop()
		s.splitter = nil
	}
	if s.dev != nil {
		s.dev.Close()
		s.dev = nil
	}
	if s.meshTun != nil {
		s.meshTun.Close()
		s.meshTun = nil
	}
	if s.forwarder != nil {
		s.forwarder.close()
		s.forwarder = nil
	}
	if s.realDev != nil {
		// Closing the wg-go tun.Device closes the underlying OS TUN fd.
		// VpnService.Builder.establish()'s PFD was already detachFd'd on
		// the Kotlin side, so we're the sole owner.
		_ = s.realDev.Close()
		s.realDev = nil
	}
}

// Hello is a smoke test — call from Kotlin to confirm the AAR loaded.
func Hello(name string) string {
	return fmt.Sprintf("hello from valhalla-mesh, %s", name)
}

// Start launches the combined tunnel WITHOUT any mesh peer. Internet
// traffic flows through the user-exit immediately; mesh-IP routes are still
// caught by the splitter but wg-go has no peers, so packets to the mesh
// subnet are silently dropped until AddPeer is called for the corresponding
// destination.
//
// Splits the previous "all-in-one" StartMesh in two so the Connect button
// can bring up the VPN with no peer (just internet) and the Files button
// can attach a peer dynamically without restarting the service.
//
// Args:
//
//	tunFD          file descriptor from VpnService.Builder.establish().detachFd()
//	wgPrivKeyB64   base64 WG private key (matches our registered pubkey)
//	selfIP         mesh IP assigned to this node
//	meshSocksAddr  SOCKS5 "host:port" — Kotlin xray's mesh-chain inbound
//	exitSocksAddr  SOCKS5 "host:port" — Kotlin xray's user-exit inbound;
//	               empty string disables non-mesh forwarding.
func Start(
	tunFD int32,
	wgPrivKeyB64 string,
	selfIP string,
	meshSocksAddr string,
	exitSocksAddr string,
) (*Session, error) {
	if meshSocksAddr == "" {
		return nil, fmt.Errorf("meshSocksAddr is required (point at Kotlin xray's mesh-chain SOCKS5 inbound)")
	}
	meshDialer, err := proxy.SOCKS5("tcp", meshSocksAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("mesh socks5 dialer: %w", err)
	}
	meshCtxDialer, ok := meshDialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("mesh socks5 dialer is not ContextDialer")
	}

	var exitCtxDialer proxy.ContextDialer
	if exitSocksAddr != "" {
		exitDialer, err := proxy.SOCKS5("tcp", exitSocksAddr, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("exit socks5 dialer: %w", err)
		}
		var dialerOK bool
		exitCtxDialer, dialerOK = exitDialer.(proxy.ContextDialer)
		if !dialerOK {
			return nil, fmt.Errorf("exit socks5 dialer is not ContextDialer")
		}
	}

	// SOCKS5 dial target is the mesh-dispatcher advertised address. The
	// xray chain on the Kotlin side carries this destination through
	// user-exit to the relay's vless-plain-in inbound; the relay routes
	// by inboundTag + (127.0.0.1, 9999) to its mesh dispatcher.
	const meshDstAddr = "127.0.0.1:9999"

	// Wrap the OS TUN fd into a wg-go tun.Device. We use this same wrapper
	// for BOTH directions — the splitter Reads from it (outgoing app pkts)
	// and the meshTun's writeBack Writes to it (decrypted peer pkts). The
	// wireguard-go wrapper integrates the fd with Go's runtime netpoller,
	// which the plain os.File path does not on Android — without this,
	// reads block forever even after the splitter is told to stop.
	realDev, _, err := tun.CreateUnmonitoredTUNFromFD(int(tunFD))
	if err != nil {
		return nil, fmt.Errorf("tun from fd: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(wgPrivKeyB64)
	if err != nil || len(privBytes) != 32 {
		_ = realDev.Close()
		return nil, fmt.Errorf("bad wg-key: must be base64 of 32 bytes")
	}
	var selfPub [pubkeyLen]byte
	derivePub(&selfPub, privBytes)

	meshTun := newVirtualTUN("vmesh", wgMTU, func(pkt []byte) error {
		_, err := realDev.Write([][]byte{pkt}, 0)
		return err
	})

	bind := NewMeshBind(selfPub, func() (net.Conn, error) {
		return meshCtxDialer.DialContext(context.Background(), "tcp", meshDstAddr)
	})

	dev := device.NewDevice(meshTun, bind, &device.Logger{
		Verbosef: func(format string, args ...any) {},
		Errorf:   func(format string, args ...any) {},
	})

	// Initial UAPI: just the private key. Peers are added later via
	// Session.AddPeer; wg-go's IpcSet without replace_peers=true merges new
	// peer blocks without disturbing existing ones.
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(privBytes))

	if err := dev.IpcSet(sb.String()); err != nil {
		dev.Close()
		meshTun.Close()
		_ = realDev.Close()
		return nil, fmt.Errorf("wg ipc: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		meshTun.Close()
		_ = realDev.Close()
		return nil, fmt.Errorf("wg up: %w", err)
	}

	var fwd *nonMeshForwarder
	if exitCtxDialer != nil {
		fwd, err = newNonMeshForwarder(exitCtxDialer, wgMTU, func(pkt []byte) error {
			_, err := realDev.Write([][]byte{pkt}, 0)
			return err
		})
		if err != nil {
			dev.Close()
			meshTun.Close()
			_ = realDev.Close()
			return nil, fmt.Errorf("non-mesh forwarder: %w", err)
		}
	}

	sp := &splitter{
		realDev: realDev,
		onMesh: func(pkt []byte) {
			// virtualTUN drops on backpressure; WG retransmits.
			meshTun.push(pkt)
		},
		onNonMesh: func(pkt []byte) {
			if fwd != nil {
				fwd.inject(pkt)
			}
			// else: silently drop. This is what TUN routes 10.100.0.0/16
			// only mode delivers anyway — onNonMesh is never invoked.
		},
	}
	go sp.run()

	_ = selfIP // reserved for diagnostics; the IP is already on the TUN via VpnService config
	return &Session{
		dev:       dev,
		meshTun:   meshTun,
		realDev:   realDev,
		splitter:  sp,
		forwarder: fwd,
	}, nil
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
