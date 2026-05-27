package registration

import (
	"bytes"
	"context"
	"encoding/json"
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

// Registrar keeps the relay visible to the control-plane by re-announcing
// itself on a 2-minute heartbeat. The relay forwards only WG ciphertext, so
// there are no credentials to fetch — the response body is ignored.
type Registrar struct {
	controlPlaneURL string
	publicAddress   string
	udpPort         int
	capacity        int
	logger          *zap.Logger
}

func New(controlPlaneURL, publicAddress string, udpPort, capacity int, logger *zap.Logger) *Registrar {
	return &Registrar{
		controlPlaneURL: controlPlaneURL,
		publicAddress:   publicAddress,
		udpPort:         udpPort,
		capacity:        capacity,
		logger:          logger,
	}
}

// Run blocks until ctx is cancelled, heartbeating every 2 minutes.
func (r *Registrar) Run(ctx context.Context) {
	if r.publicAddress == "" {
		r.logger.Warn("PUBLIC_ADDRESS not set, skipping control plane registration")
		return
	}

	url := strings.TrimRight(r.controlPlaneURL, "/") + "/api/v1/internal/relay/register"

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	r.registerOnce(ctx, url)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.registerOnce(ctx, url)
		}
	}
}

func (r *Registrar) registerOnce(ctx context.Context, url string) {
	body, _ := json.Marshal(relayRegisterRequest{
		Address:  r.publicAddress,
		Port:     r.udpPort,
		Capacity: r.capacity,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		r.logger.Error("create registration request failed", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.logger.Warn("control plane registration failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		r.logger.Warn("registration returned error", zap.Int("status", resp.StatusCode))
		return
	}

	r.logger.Info("registered with control plane",
		zap.String("address", r.publicAddress),
		zap.Int("udp_port", r.udpPort))
}
