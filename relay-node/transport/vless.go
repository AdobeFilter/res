package transport

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"
)

// VLESSRelay wraps the relay transport in VLESS+Reality for censorship resistance.
// Traffic appears as normal TLS 1.3 to external observers.
type VLESSRelay struct {
	listenPort int
	enabled    bool
	logger     *zap.Logger
}

func NewVLESSRelay(listenPort int, logger *zap.Logger) *VLESSRelay {
	return &VLESSRelay{
		listenPort: listenPort,
		logger:     logger,
	}
}

// XrayConfig generates the xray-core relay configuration.
// The relay acts as a VLESS inbound -> freedom outbound (to forward to actual destination).
func (v *VLESSRelay) XrayConfig(uuid, realityPrivKey string, realityShortIDs []string) map[string]interface{} {
	return map[string]interface{}{
		"inbounds": []map[string]interface{}{
			{
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
						"dest":        "microsoft.com:443",
						"serverNames": []string{"microsoft.com", "www.microsoft.com"},
						"privateKey":  realityPrivKey,
						"shortIds":    realityShortIDs,
					},
				},
			},
		},
		"outbounds": []map[string]interface{}{
			{
				"protocol": "freedom",
				"tag":      "direct",
			},
		},
	}
}

// Start begins the VLESS+Reality transport for the relay.
func (v *VLESSRelay) Start(ctx context.Context, uuid, realityPrivKey string, realityShortIDs []string) error {
	config := v.XrayConfig(uuid, realityPrivKey, realityShortIDs)
	configJSON, _ := json.MarshalIndent(config, "", "  ")

	v.logger.Info("starting VLESS+Reality relay transport",
		zap.Int("port", v.listenPort),
		zap.String("config", string(configJSON)))

	// NOTE: Same as exit-node — full xray-core integration will use:
	//   instance, err := xcore.New(xrayConfig)
	//   err = instance.Start()

	v.enabled = true
	return nil
}

// Stop shuts down the VLESS relay transport.
func (v *VLESSRelay) Stop() {
	v.enabled = false
	v.logger.Info("VLESS+Reality relay transport stopped")
}

// IsEnabled returns whether the VLESS transport is active.
func (v *VLESSRelay) IsEnabled() bool {
	return v.enabled
}
