package handler

import (
	"net/http"

	"go.uber.org/zap"
	"valhalla/control-plane/db"
)

// DeviceHandler exposes device-scoped endpoints that don't require an
// authenticated session. Used pre-login to streamline onboarding (e.g.
// auto-fill the email field once we know which account this device is
// linked to).
type DeviceHandler struct {
	nodes    db.NodeRepository
	accounts db.AccountRepository
	logger   *zap.Logger
}

func NewDeviceHandler(nodes db.NodeRepository, accounts db.AccountRepository, logger *zap.Logger) *DeviceHandler {
	return &DeviceHandler{nodes: nodes, accounts: accounts, logger: logger}
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
