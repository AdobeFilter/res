package handler

import (
	"net/http"

	"go.uber.org/zap"
	"valhalla/control-plane/db"
	"valhalla/control-plane/middleware"
	"valhalla/control-plane/remnawave"
)

// DeviceHandler exposes device-scoped endpoints that don't require an
// authenticated session. Used pre-login to streamline onboarding (e.g.
// auto-fill the email field once we know which account this device is
// linked to). Also serves the authenticated quota endpoint.
type DeviceHandler struct {
	nodes     db.NodeRepository
	accounts  db.AccountRepository
	remnawave *remnawave.Client
	logger    *zap.Logger
}

func NewDeviceHandler(nodes db.NodeRepository, accounts db.AccountRepository, rw *remnawave.Client, logger *zap.Logger) *DeviceHandler {
	return &DeviceHandler{nodes: nodes, accounts: accounts, remnawave: rw, logger: logger}
}

// AccountHint handles GET /api/v1/devices/account-hint?device_id=...
// Returns {"email": "..."} when the device is bound to an account, 404 when
// it isn't. The endpoint is intentionally unauthenticated — its purpose is
// to pre-fill the login form. It only leaks email-given-device_id, and
// obtaining a device_id (ANDROID_ID) requires either physical access to the
// device or app-level malware, at which point the attacker has bigger
// problems than the email leak.
func (h *DeviceHandler) AccountHint(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id query param required")
		return
	}

	node, err := h.nodes.FindByDeviceIDGlobal(r.Context(), deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "device not linked")
		return
	}

	account, err := h.accounts.GetByID(r.Context(), node.AccountID)
	if err != nil {
		h.logger.Warn("hint: device linked to non-existent account",
			zap.String("device_id", deviceID),
			zap.String("account_id", node.AccountID))
		writeError(w, http.StatusNotFound, "device not linked")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"email": account.Email})
}

// Quota handles GET /api/v1/accounts/me/quota — authenticated.
// Returns the current account's monthly subscription state pulled live from
// Remnawave: bytes used so far + total quota. The Android TrafficBar polls
// this. When Remnawave isn't configured (dogfood / standalone), returns the
// free-tier defaults so the bar still renders sensibly.
func (h *DeviceHandler) Quota(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	type quotaResponse struct {
		BytesUsed       int64  `json:"bytes_used"`
		BytesTotal      int64  `json:"bytes_total"`
		Tier            string `json:"tier"`
		SubscriptionURL string `json:"subscription_url,omitempty"`
	}

	account, err := h.accounts.GetByID(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}

	// Standalone mode or account that wasn't linked: return free-tier zeros.
	if !h.remnawave.Enabled() || account.RemnawaveUUID == "" {
		writeJSON(w, http.StatusOK, quotaResponse{
			BytesUsed: 0, BytesTotal: remnawave.FreeTierBytes,
			Tier: account.Tier, SubscriptionURL: account.SubscriptionURL,
		})
		return
	}

	user, err := h.remnawave.GetUser(account.RemnawaveUUID)
	if err != nil {
		h.logger.Warn("remnawave quota fetch failed",
			zap.String("account_id", accountID), zap.Error(err))
		// Fail soft so the UI doesn't pop a scary error — return the limit
		// without usage and let the next poll retry.
		writeJSON(w, http.StatusOK, quotaResponse{
			BytesUsed: 0, BytesTotal: remnawave.FreeTierBytes,
			Tier: account.Tier, SubscriptionURL: account.SubscriptionURL,
		})
		return
	}

	writeJSON(w, http.StatusOK, quotaResponse{
		BytesUsed:       user.UserTraffic.UsedTrafficBytes,
		BytesTotal:      user.TrafficLimit,
		Tier:            account.Tier,
		SubscriptionURL: user.SubscriptionURL,
	})
}
