package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type stunRegisterRequest struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// RegisterWithControlPlane registers this STUN server with the control plane.
// It re-registers every 2 minutes to keep the registration alive.
func RegisterWithControlPlane(ctx context.Context, controlPlaneURL, publicAddress, listenAddr string, logger *zap.Logger) {
	port := 3478
	if parts := strings.Split(listenAddr, ":"); len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}

	if publicAddress == "" {
		logger.Warn("PUBLIC_ADDRESS not set, skipping control plane registration")
		return
	}

	url := strings.TrimRight(controlPlaneURL, "/") + "/api/v1/internal/stun/register"

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	register := func() {
		body, _ := json.Marshal(stunRegisterRequest{
			Address: publicAddress,
			Port:    port,
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

		if resp.StatusCode != http.StatusOK {
			logger.Warn("control plane registration returned error",
				zap.Int("status", resp.StatusCode))
			return
		}

		logger.Info("registered with control plane",
			zap.String("address", publicAddress),
			zap.Int("port", port))
	}

	// Register immediately
	register()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			register()
		}
	}

	_ = fmt.Sprintf // suppress unused import
}
