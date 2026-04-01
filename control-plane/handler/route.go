package handler

import (
	"net/http"

	"go.uber.org/zap"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/service"
)

type RouteHandler struct {
	routeService *service.RouteService
	stunRepo     db.STUNServerRepository
	logger       *zap.Logger
}

func NewRouteHandler(routeService *service.RouteService, stunRepo db.STUNServerRepository, logger *zap.Logger) *RouteHandler {
	return &RouteHandler{routeService: routeService, stunRepo: stunRepo, logger: logger}
}

// GetOptimal handles GET /api/v1/routes/optimal?from=X&to=Y
func (h *RouteHandler) GetOptimal(w http.ResponseWriter, r *http.Request) {
	fromNodeID := r.URL.Query().Get("from")
	toNodeID := r.URL.Query().Get("to")

	if fromNodeID == "" || toNodeID == "" {
		writeError(w, http.StatusBadRequest, "from and to query params are required")
		return
	}

	route, err := h.routeService.GetOptimalRoute(r.Context(), fromNodeID, toNodeID)
	if err != nil {
		h.logger.Error("get optimal route failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "no route available")
		return
	}

	writeJSON(w, http.StatusOK, route)
}

// GetSTUNServers handles GET /api/v1/routes/stun-servers
func (h *RouteHandler) GetSTUNServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.stunRepo.GetAll(r.Context())
	if err != nil {
		h.logger.Error("get STUN servers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get STUN servers")
		return
	}

	writeJSON(w, http.StatusOK, protocol.STUNServersResponse{Servers: servers})
}
