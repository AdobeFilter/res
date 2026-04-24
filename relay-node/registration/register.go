package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Credentials are what the control-plane hands back on successful relay
// registration. The same values arrive on every re-registration; we only
// surface them once via Credentials() because xray is already running.
type Credentials struct {
	VLESSUUID         string `json:"vless_uuid"`
	RealityPrivateKey string `json:"reality_private_key"`
	RealityPublicKey  string `json:"reality_public_key"`
	RealityShortIDs   string `json:"reality_short_ids"`
	RealitySNI        string `json:"reality_sni"`
}

type relayRegisterRequest struct {
	Address   string `json:"address"`
	Port      int    `json:"port"`
	VLESSPort int    `json:"vless_port"`
	Capacity  int    `json:"capacity"`
}

// Registrar keeps the relay visible to the control-plane by re-announcing
// itself on a 2-minute heartbeat, and publishes Reality credentials received
// on the first successful registration.
type Registrar struct {
	controlPlaneURL string
	publicAddress   string
	udpPort         int
	vlessPort       int
	capacity        int
	logger          *zap.Logger

	credsOnce sync.Once
	credsCh   chan Credentials
}

func New(controlPlaneURL, publicAddress string, udpPort, vlessPort, capacity int, logger *zap.Logger) *Registrar {
	return &Registrar{
		controlPlaneURL: controlPlaneURL,
		publicAddress:   publicAddress,
		udpPort:         udpPort,
		vlessPort:       vlessPort,
		capacity:        capacity,
		logger:          logger,
		credsCh:         make(chan Credentials, 1),
	}
}

// Credentials returns a channel that receives the Reality credentials exactly
// once, on the first successful registration. Consumers that start after
// credentials were published still receive them because the channel is
// buffered and only written once.
func (r *Registrar) Credentials() <-chan Credentials {
	return r.credsCh
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
		Address:   r.publicAddress,
		Port:      r.udpPort,
		VLESSPort: r.vlessPort,
		Capacity:  r.capacity,
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

	var creds Credentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		r.logger.Warn("decode registration response failed", zap.Error(err))
		return
	}

	r.logger.Info("registered with control plane",
		zap.String("address", r.publicAddress),
		zap.Int("udp_port", r.udpPort),
		zap.Int("vless_port", r.vlessPort))

	// Publish credentials exactly once — xray only needs to start once.
	r.credsOnce.Do(func() {
		if creds.VLESSUUID != "" && creds.RealityPrivateKey != "" {
			r.credsCh <- creds
		}
	})
}
