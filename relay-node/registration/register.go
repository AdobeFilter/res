package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

type relayRegisterRequest struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Capacity int    `json:"capacity"`
}

// RegisterWithControlPlane registers this relay with the control plane.
func RegisterWithControlPlane(ctx context.Context, controlPlaneURL, publicAddress string, port, capacity int, logger *zap.Logger) {
	if publicAddress == "" {
		logger.Warn("PUBLIC_ADDRESS not set, skipping control plane registration")
		return
	}

	url := strings.TrimRight(controlPlaneURL, "/") + "/api/v1/internal/relay/register"

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	register := func() {
		body, _ := json.Marshal(relayRegisterRequest{
			Address:  publicAddress,
			Port:     port,
			Capacity: capacity,
		})

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			logger.Error("create registration request failed", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Warn("control plane registration failed", zap.Error(err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			logger.Info("registered with control plane",
				zap.String("address", publicAddress),
				zap.Int("port", port))
		} else {
			logger.Warn("registration returned error", zap.Int("status", resp.StatusCode))
		}
	}

	register()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			register()
		}
	}

	_ = fmt.Sprintf // keep import
}
