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
//   - spawn xray (libv2ray) with SOCKS5 inbound + VLESS+Reality outbound to user's exit-node
//   - call StartMesh(...) with the FD and SOCKS5 address
//
// Go responsibilities (this AAR):
//   - dial control plane (through SOCKS5) for the optimal route + relay creds
//   - dial relay :8444 (through SOCKS5) and run the mesh-frame protocol
//   - run wireguard-go on the TUN FD with a custom Bind that funnels every
//     WG packet over that single TCP stream
package mesh

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

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

// StartMesh starts the mesh tunnel.
//
// Args:
//
//	tunFD         file descriptor from VpnService.Builder.establish().detachFd()
//	controlURL    e.g. "http://144.48.10.51:8443"
//	token         account auth token (Bearer)
//	selfNodeID    this device's node ID
//	targetNodeID  peer node ID to mesh with
//	wgPrivKeyB64  base64 WG private key (matches the pubkey registered for selfNodeID)
//	selfIP        mesh IP assigned to this node (e.g. "10.100.0.20")
//	socksAddr     SOCKS5 proxy "host:port" used for both control-plane HTTP and
//	              relay TCP. Must be non-empty — Android always chains through
//	              the Kotlin-managed xray (the relay's IP is DPI-blocked from
//	              typical RU networks).
func StartMesh(
	tunFD int32,
	controlURL string,
	token string,
	selfNodeID string,
	targetNodeID string,
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

	httpClient := &http.Client{
		Transport: &http.Transport{DialContext: ctxDialer.DialContext},
		Timeout:   15 * time.Second,
	}

	route, err := fetchRoute(httpClient, controlURL, token, selfNodeID, targetNodeID)
	if err != nil {
		return nil, fmt.Errorf("fetch route: %w", err)
	}
	if route.Relay == nil {
		return nil, fmt.Errorf("control plane did not return relay credentials")
	}

	// Plain-VLESS port on the relay — Reality stays direct-only on :443; the
	// chained path always uses :8444 (matches relay-node/transport/vless.go).
	relayHostPort := net.JoinHostPort(route.Relay.Address, "8444")

	// Newer wg-go returns (tun, name, err); name is unused on Android (the
	// caller already named the interface via VpnService.Builder).
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
		return ctxDialer.DialContext(context.Background(), "tcp", relayHostPort)
	})

	dev := device.NewDevice(tunDev, bind, &device.Logger{
		Verbosef: func(format string, args ...any) {},
		Errorf:   func(format string, args ...any) {},
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(privBytes))
	fmt.Fprintf(&sb, "public_key=%s\n", base64ToHex(route.DstPeer.PublicKey))
	fmt.Fprintf(&sb, "endpoint=vmesh:%s\n", base64ToHex(route.DstPeer.PublicKey))
	fmt.Fprintf(&sb, "allowed_ip=%s/32\n", route.DstPeer.InternalIP)
	fmt.Fprintf(&sb, "persistent_keepalive_interval=25\n")

	if err := dev.IpcSet(sb.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg ipc: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wg up: %w", err)
	}

	return &Session{dev: dev}, nil
}

// --- helpers ---

type relayEndpoint struct {
	Address          string `json:"address"`
	VLESSPort        int    `json:"vless_port"`
	VLESSUUID        string `json:"vless_uuid"`
	RealityPublicKey string `json:"reality_public_key"`
	RealitySNI       string `json:"reality_sni"`
	RealityShortID   string `json:"reality_short_id"`
}

type peerInfo struct {
	PublicKey  string `json:"public_key"`
	Endpoint   string `json:"endpoint,omitempty"`
	InternalIP string `json:"internal_ip"`
}

type routeResponse struct {
	ConnectionType string         `json:"connection_type"`
	DstPeer        peerInfo       `json:"dst_peer"`
	Relay          *relayEndpoint `json:"relay,omitempty"`
}

func fetchRoute(c *http.Client, base, token, from, to string) (*routeResponse, error) {
	u := fmt.Sprintf("%s/api/v1/routes/optimal?from=%s&to=%s", base, from, to)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("control plane %d: %s", resp.StatusCode, string(body))
	}
	var rr routeResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, err
	}
	return &rr, nil
}

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
