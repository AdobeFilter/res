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

type relayRegisterRequest struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Capacity int    `json:"capacity"`
}

// relayRegisterResponse only decodes the field the relay actually uses.
// Control-plane returns more (legacy VLESS/Reality credentials for relays that
// still run xray) — we ignore all of it.
type relayRegisterResponse struct {
	MeshAuthKey string `json:"mesh_auth_key"`
}

// Registrar keeps the relay visible to the control-plane by re-announcing
// itself on a 2-minute heartbeat. It also receives the shared mesh-auth HMAC
// secret in the register response and exposes it via MeshAuthKey so the
// dispatcher can verify mesh-tokens locally. Storing it once in CP's env (not
// per relay) keeps the secret in one place and removes operator input on the
// relay side.
type Registrar struct {
	controlPlaneURL string
	publicAddress   string
	udpPort         int
	capacity        int
	logger          *zap.Logger

	keyMu       sync.RWMutex
	meshAuthKey []byte
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

// MeshAuthKey returns the current shared HMAC secret. Safe to call before the
// first successful registration (returns nil until then); safe to pass as a
// keyProvider to the mesh dispatcher.
func (r *Registrar) MeshAuthKey() []byte {
	r.keyMu.RLock()
	defer r.keyMu.RUnlock()
	if len(r.meshAuthKey) == 0 {
		return nil
	}
	// Hand back a copy — caller is the dispatcher and isn't expected to
	// mutate, but the cost is trivial and prevents data-race surprises.
	out := make([]byte, len(r.meshAuthKey))
	copy(out, r.meshAuthKey)
	return out
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

	var parsed relayRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		r.logger.Warn("decode register response failed", zap.Error(err))
		return
	}

	r.keyMu.Lock()
	prev := len(r.meshAuthKey) > 0
	r.meshAuthKey = []byte(parsed.MeshAuthKey)
	now := len(r.meshAuthKey) > 0
	r.keyMu.Unlock()

	if !prev && now {
		r.logger.Info("mesh auth key received from control-plane — token enforcement ON")
	} else if prev && !now {
		r.logger.Warn("mesh auth key cleared by control-plane — token enforcement OFF")
	}

	r.logger.Info("registered with control plane",
		zap.String("address", r.publicAddress),
		zap.Int("udp_port", r.udpPort),
		zap.Bool("token_auth", now))
}
