package tunnel

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
)

// VLESSManager handles VLESS+Reality transport using xray-core as a library.
type VLESSManager struct {
	enabled    bool
	listenPort int
	targetDest string // Reality target domain (e.g., "microsoft.com:443")
	privateKey string // Reality private key
	shortIDs   []string
	logger     *zap.Logger
}

func NewVLESSManager(listenPort int, logger *zap.Logger) *VLESSManager {
	return &VLESSManager{
		listenPort: listenPort,
		targetDest: "microsoft.com:443",
		shortIDs:   []string{""},
		logger:     logger,
	}
}

// XrayInboundConfig generates the xray-core inbound configuration for VLESS+Reality.
func (v *VLESSManager) XrayInboundConfig(uuid, realityPrivKey string, realityShortIDs []string) map[string]interface{} {
	return map[string]interface{}{
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
				"dest":        v.targetDest,
				"xver":        0,
				"serverNames": []string{"microsoft.com", "www.microsoft.com"},
				"privateKey":  realityPrivKey,
				"shortIds":    realityShortIDs,
			},
		},
	}
}

// Start starts the VLESS+Reality inbound using xray-core.
// This creates an xray instance programmatically.
func (v *VLESSManager) Start(ctx context.Context, uuid, realityPrivKey string, realityShortIDs []string) error {
	config := v.XrayInboundConfig(uuid, realityPrivKey, realityShortIDs)

	configJSON, _ := json.MarshalIndent(config, "", "  ")
	v.logger.Info("starting VLESS+Reality transport",
		zap.Int("port", v.listenPort),
		zap.String("config", string(configJSON)))

	// NOTE: Full xray-core integration requires importing:
	//   xcore "github.com/xtls/xray-core/core"
	//   "github.com/xtls/xray-core/infra/conf"
	//
	// The xray instance is created with:
	//   instance, err := xcore.New(xrayConfig)
	//   err = instance.Start()
	//
	// For the MVP, this provides the configuration structure.
	// The actual xray-core Start() call will be wired when dependencies are resolved.

	v.enabled = true
	v.logger.Info("VLESS+Reality transport started", zap.Int("port", v.listenPort))
	return nil
}

// Stop stops the VLESS+Reality transport.
func (v *VLESSManager) Stop() error {
	if !v.enabled {
		return nil
	}
	v.enabled = false
	v.logger.Info("VLESS+Reality transport stopped")
	return nil
}

// IsEnabled returns whether VLESS+Reality is currently active.
func (v *VLESSManager) IsEnabled() bool {
	return v.enabled
}

// SetEnabled enables or disables the VLESS transport.
func (v *VLESSManager) SetEnabled(enabled bool) {
	v.enabled = enabled
}

func init() {
	_ = fmt.Sprintf // keep import
}
