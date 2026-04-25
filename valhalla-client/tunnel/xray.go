package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"valhalla/common/protocol"
)

// XrayClient runs xray as a subprocess with a dokodemo-door loopback
// inbound and a VLESS+Reality outbound pinned to the assigned relay. The
// MeshBind then dials the dokodemo inbound to get a TCP stream that
// xray transparently wraps in VLESS+Reality on its way to the relay.
//
// If exit is non-nil, the relay outbound is chained through a second
// VLESS+Reality outbound (the user's exit-node) via xray's
// proxySettings.tag. On the wire there is then exactly one TLS handshake
// visible — to the exit-node's SNI — and the relay-side Reality handshake
// rides inside that encrypted channel. Used when the relay's IP itself is
// blacklisted by DPI.
type XrayClient struct {
	xrayBin     string
	logger      *zap.Logger
	localAddr   string // dokodemo inbound: "127.0.0.1:PORT"
	meshDstAddr string // destination echoed back by the relay dispatcher — must match its listen addr (e.g. 127.0.0.1:9999)
	relay       *protocol.RelayEndpoint
	exit        *ExitNode // optional; when set, relay outbound chains through it

	mu      sync.Mutex
	cmd     *exec.Cmd
	cfgPath string
	running bool
}

// NewXrayClient prepares (but does not start) an xray subprocess. localAddr
// is the TCP host:port the MeshBind will dial; pick any free loopback port.
// meshDstAddr is the loopback address the relay's mesh dispatcher listens
// on (passed through in xray's VLESS CONNECT header). exit may be nil for
// direct relay access (no DPI in the way).
func NewXrayClient(xrayBin, localAddr, meshDstAddr string, relay *protocol.RelayEndpoint, exit *ExitNode, logger *zap.Logger) *XrayClient {
	if xrayBin == "" {
		xrayBin = "xray"
	}
	return &XrayClient{
		xrayBin:     xrayBin,
		logger:      logger,
		localAddr:   localAddr,
		meshDstAddr: meshDstAddr,
		relay:       relay,
		exit:        exit,
	}
}

// Start writes the xray config to a tempdir and spawns the subprocess.
// Returns once xray prints its first log line (a rough proxy for
// "listener is up"). Callers should Stop on shutdown.
func (x *XrayClient) Start(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.running {
		return nil
	}

	cfg, err := x.buildConfig()
	if err != nil {
		return err
	}
	j, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal xray config: %w", err)
	}

	dir, err := os.MkdirTemp("", "valhalla-client-xray-*")
	if err != nil {
		return fmt.Errorf("tempdir: %w", err)
	}
	path := filepath.Join(dir, "xray.json")
	if err := os.WriteFile(path, j, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	cmd := exec.CommandContext(ctx, x.xrayBin, "run", "-c", path)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}

	x.cmd = cmd
	x.cfgPath = path
	x.running = true
	go x.pipeLog(stdout, "xray.stdout")
	go x.pipeLog(stderr, "xray.stderr")

	// Give the dokodemo inbound a moment to bind. We poll with a tight
	// timeout rather than sleeping blindly — if xray dies on bad config,
	// we fail fast instead of waiting for the full grace period.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", x.localAddr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			x.logger.Info("xray client up",
				zap.String("local", x.localAddr),
				zap.String("relay", fmt.Sprintf("%s:%d", x.relay.Address, x.relay.VLESSPort)))
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("xray dokodemo inbound did not come up on %s", x.localAddr)
}

func (x *XrayClient) Stop() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.running || x.cmd == nil {
		return
	}
	if x.cmd.Process != nil {
		_ = x.cmd.Process.Kill()
	}
	_ = x.cmd.Wait()
	if x.cfgPath != "" {
		_ = os.RemoveAll(filepath.Dir(x.cfgPath))
	}
	x.running = false
}

func (x *XrayClient) pipeLog(r io.Reader, tag string) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
				if line != "" {
					x.logger.Info(tag, zap.String("msg", line))
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// buildConfig produces the xray JSON: one dokodemo inbound on localAddr
// with destination locked to meshDstAddr, and one VLESS+Reality outbound
// aimed at the relay. Every TCP stream on the inbound is tunneled to the
// relay via Reality, and on the relay side VLESS's CONNECT header carries
// meshDstAddr so the relay's routing pins it to the mesh dispatcher.
func (x *XrayClient) buildConfig() (map[string]interface{}, error) {
	host, port, err := splitHostPort(x.localAddr)
	if err != nil {
		return nil, fmt.Errorf("local addr: %w", err)
	}
	dstHost, dstPort, err := splitHostPort(x.meshDstAddr)
	if err != nil {
		return nil, fmt.Errorf("mesh dst addr: %w", err)
	}

	relayOutbound := map[string]interface{}{
		"tag":      "relay",
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": x.relay.Address,
					"port":    x.relay.VLESSPort,
					"users": []map[string]interface{}{
						{
							"id":         x.relay.VLESSUUID,
							"flow":       "xtls-rprx-vision",
							"encryption": "none",
						},
					},
				},
			},
		},
		"streamSettings": map[string]interface{}{
			"network":  "tcp",
			"security": "reality",
			"realitySettings": map[string]interface{}{
				"fingerprint": "chrome",
				"serverName":  x.relay.RealitySNI,
				"publicKey":   x.relay.RealityPublicKey,
				"shortId":     x.relay.RealityShortID,
			},
		},
	}

	outbounds := []map[string]interface{}{relayOutbound}

	// Chain through user-exit when DPI sits between us and the relay's IP.
	// The relay outbound now dials its destination (45.x.y.z:443) THROUGH
	// the user-exit outbound — DPI sees only one TLS to the exit-node SNI.
	if x.exit != nil {
		relayOutbound["proxySettings"] = map[string]interface{}{"tag": "user-exit"}
		outbounds = append(outbounds, buildVLESSOutbound("user-exit", x.exit))
	}

	return map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "info"},
		"inbounds": []map[string]interface{}{
			{
				"tag":      "mesh-in",
				"listen":   host,
				"port":     port,
				"protocol": "dokodemo-door",
				"settings": map[string]interface{}{
					"address": dstHost,
					"port":    dstPort,
					"network": "tcp",
				},
			},
		},
		"outbounds": outbounds,
		"routing": map[string]interface{}{
			"rules": []map[string]interface{}{
				{"type": "field", "inboundTag": []string{"mesh-in"}, "outboundTag": "relay"},
			},
		},
	}, nil
}

// buildVLESSOutbound produces a VLESS+Reality outbound config from an
// ExitNode. Mirrors the shape of the relay outbound (same protocol, same
// stream settings) so the chained pair is symmetric to xray's router.
func buildVLESSOutbound(tag string, e *ExitNode) map[string]interface{} {
	reality := map[string]interface{}{
		"fingerprint": e.Fingerprint,
		"serverName":  e.SNI,
		"publicKey":   e.PublicKey,
		"shortId":     e.ShortID,
	}
	if e.SpiderX != "" {
		reality["spiderX"] = e.SpiderX
	}
	user := map[string]interface{}{
		"id":         e.UUID,
		"encryption": "none",
	}
	if e.Flow != "" {
		user["flow"] = e.Flow
	}
	return map[string]interface{}{
		"tag":      tag,
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": e.Address,
					"port":    e.Port,
					"users":   []map[string]interface{}{user},
				},
			},
		},
		"streamSettings": map[string]interface{}{
			"network":         e.Network,
			"security":        e.Security,
			"realitySettings": reality,
		},
	}
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	p, err := netAtoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, p, nil
}

func netAtoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a port: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
