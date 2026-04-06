package handler

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/middleware"
)

type SettingsHandler struct {
	settings db.AccountSettingsRepository
	nodes    db.NodeRepository
	logger   *zap.Logger
}

func NewSettingsHandler(settings db.AccountSettingsRepository, nodes db.NodeRepository, logger *zap.Logger) *SettingsHandler {
	return &SettingsHandler{settings: settings, nodes: nodes, logger: logger}
}

// GetSettings handles GET /api/v1/accounts/{id}/settings
func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	pathAccountID := extractPathParam(r.URL.Path, "/api/v1/accounts/")
	pathAccountID = extractPathParam(pathAccountID, "") // handle /settings suffix

	// Verify ownership
	if accountID != pathAccountID && pathAccountID != "" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	settings, err := h.settings.Get(r.Context(), accountID)
	if err != nil {
		h.logger.Error("get settings failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get settings")
		return
	}

	writeJSON(w, http.StatusOK, protocol.SettingsResponse{Settings: *settings})
}

// UpdateSettings handles PUT /api/v1/accounts/{id}/settings
func (h *SettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())

	var req protocol.UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.VLESSEnabled == nil && req.ExitNodeID == nil {
		writeError(w, http.StatusBadRequest, "no settings to update")
		return
	}

	var settings *api.AccountSettings
	var err error

	if req.ExitNodeID != nil {
		exitID := *req.ExitNodeID
		var exitPtr *string
		if exitID != "" {
			exitPtr = &exitID
		}
		settings, err = h.settings.SetExitNode(r.Context(), accountID, exitPtr)
		if err != nil {
			h.logger.Error("set exit node failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to set exit node")
			return
		}
	}

	if req.VLESSEnabled != nil {
		settings, err = h.settings.Upsert(r.Context(), accountID, *req.VLESSEnabled)
		if err != nil {
			h.logger.Error("update settings failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to update settings")
			return
		}
	}

	if settings == nil {
		settings, _ = h.settings.Get(r.Context(), accountID)
	}

	writeJSON(w, http.StatusOK, protocol.SettingsResponse{Settings: *settings})
}

// GetDevices handles GET /api/v1/accounts/{id}/devices
func (h *SettingsHandler) GetDevices(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())

	nodes, err := h.nodes.GetByAccountID(r.Context(), accountID)
	if err != nil {
		h.logger.Error("get devices failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get devices")
		return
	}

	var devices []protocol.DevicesResponse
	_ = devices // use protocol.DevicesResponse in the response

	writeJSON(w, http.StatusOK, nodes)
}
