package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// VLESSRelay runs xray-core as a subprocess with a VLESS+Reality inbound, so
// clients behind DPI can reach this relay via a tunnel that looks like plain
// TLS traffic to a well-known SNI. For Phase 1 the outbound is `freedom`
// (pass-through) — reverse-proxy routing to specific devices comes in a
// later phase.
type VLESSRelay struct {
	listenPort int
	xrayBin    string
	logger     *zap.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	cfgPath string
	running bool
}

// NewVLESSRelay creates an unstarted relay. xrayBin is the absolute path to
// the xray binary; empty string falls back to looking up "xray" in $PATH.
func NewVLESSRelay(listenPort int, xrayBin string, logger *zap.Logger) *VLESSRelay {
	if xrayBin == "" {
		xrayBin = "xray"
	}
	return &VLESSRelay{
		listenPort: listenPort,
		xrayBin:    xrayBin,
		logger:     logger,
	}
}

// Start spawns xray with the given credentials. Idempotent: calling Start on
// an already-running instance returns nil. Callers should pair every Start
// with a Stop before reconfiguring.
func (v *VLESSRelay) Start(ctx context.Context, uuid, realityPrivKey, realityPubKey, realityShortIDs, realitySNI string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.running {
		return nil
	}

	if uuid == "" || realityPrivKey == "" {
		return errors.New("vless relay: missing credentials")
	}

	cfg := v.buildConfig(uuid, realityPrivKey, realityShortIDs, realitySNI)
	cfgJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal xray config: %w", err)
	}

	cfgDir, err := os.MkdirTemp("", "valhalla-xray-*")
	if err != nil {
		return fmt.Errorf("tempdir: %w", err)
	}
	cfgPath := filepath.Join(cfgDir, "xray.json")
	if err := os.WriteFile(cfgPath, cfgJSON, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	cmd := exec.CommandContext(ctx, v.xrayBin, "run", "-c", cfgPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}

	v.cmd = cmd
	v.cfgPath = cfgPath
	v.running = true

	go v.pipeLog(stdout, "xray.stdout")
	go v.pipeLog(stderr, "xray.stderr")
	go v.supervise(ctx)

	v.logger.Info("VLESS+Reality relay started",
		zap.Int("port", v.listenPort),
		zap.String("sni", realitySNI),
		zap.String("reality_pbk", realityPubKey),
		zap.Int("pid", cmd.Process.Pid))
	return nil
}

// Stop kills the xray subprocess (if any) and cleans up the config file.
func (v *VLESSRelay) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.running || v.cmd == nil {
		return
	}
	if v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	}
	_ = v.cmd.Wait()
	if v.cfgPath != "" {
		_ = os.RemoveAll(filepath.Dir(v.cfgPath))
	}
	v.running = false
	v.logger.Info("VLESS+Reality relay stopped")
}

// IsRunning reports whether the xray subprocess is alive.
func (v *VLESSRelay) IsRunning() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.running
}

// supervise watches the subprocess and marks it stopped when it exits. We
// don't auto-restart — if xray dies, that's typically a config problem and
// thrashing makes it harder to diagnose. Instead we log loudly and let the
// next health-check surface the failure.
func (v *VLESSRelay) supervise(ctx context.Context) {
	if v.cmd == nil {
		return
	}
	err := v.cmd.Wait()

	v.mu.Lock()
	v.running = false
	v.mu.Unlock()

	if ctx.Err() == nil && err != nil {
		v.logger.Error("xray exited unexpectedly", zap.Error(err))
	} else {
		v.logger.Info("xray exited")
	}
}

func (v *VLESSRelay) pipeLog(r io.Reader, tag string) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			lines := strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				v.logger.Info(tag, zap.String("msg", line))
			}
		}
		if err != nil {
			return
		}
	}
}

// buildConfig produces the xray JSON for Phase 1: one VLESS+Reality inbound
// on :listenPort, one freedom outbound. No reverse-proxy routing yet — that
// wires in when we're ready to dispatch traffic to specific mesh peers.
func (v *VLESSRelay) buildConfig(uuid, realityPrivKey, shortIDsCSV, sni string) map[string]interface{} {
	shortIDs := splitCSV(shortIDsCSV)
	serverNames := []string{sni}
	return map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"inbounds": []map[string]interface{}{
			{
				"tag":      "vless-in",
				"listen":   "0.0.0.0",
				"port":     v.listenPort,
				"protocol": "vless",
				"settings": map[string]interface{}{
					"clients": []map[string]interface{}{
						{
							"id":   uuid,
							"flow": "xtls-rprx-vision",
						},
					},
					"decryption": "none",
				},
				"streamSettings": map[string]interface{}{
					"network":  "tcp",
					"security": "reality",
					"realitySettings": map[string]interface{}{
						"show":        false,
						"dest":        sni + ":443",
						"serverNames": serverNames,
						"privateKey":  realityPrivKey,
						"shortIds":    shortIDs,
					},
				},
			},
		},
		"outbounds": []map[string]interface{}{
			{
				"tag":      "direct",
				"protocol": "freedom",
				"settings": map[string]interface{}{},
			},
		},
		"routing": map[string]interface{}{
			"rules": []map[string]interface{}{
				{"type": "field", "inboundTag": []string{"vless-in"}, "outboundTag": "direct"},
			},
		},
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return []string{""}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		out = append(out, p)
	}
	return out
}

var _ = time.Second // reserved for future health-check loops
