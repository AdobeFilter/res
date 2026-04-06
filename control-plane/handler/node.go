package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/middleware"
	"valhalla/control-plane/service"
)

type NodeHandler struct {
	nodeService *service.NodeService
	nodes       db.NodeRepository
	logger      *zap.Logger
}

func NewNodeHandler(nodeService *service.NodeService, nodes db.NodeRepository, logger *zap.Logger) *NodeHandler {
	return &NodeHandler{nodeService: nodeService, nodes: nodes, logger: logger}
}

// Register handles POST /api/v1/nodes/register
func (h *NodeHandler) Register(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req protocol.NodeRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.PublicKey == "" || req.NodeType == "" {
		writeError(w, http.StatusBadRequest, "name, node_type, and public_key are required")
		return
	}

	resp, err := h.nodeService.RegisterNode(r.Context(), accountID, req)
	if err != nil {
		h.logger.Error("register node failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to register node")
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /api/v1/nodes
func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	nodes, err := h.nodes.GetByAccountID(r.Context(), accountID)
	if err != nil {
		h.logger.Error("list nodes failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to list nodes")
		return
	}

	writeJSON(w, http.StatusOK, nodes)
}

// Delete handles DELETE /api/v1/nodes/{id}
func (h *NodeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	nodeID := extractPathParam(r.URL.Path, "/api/v1/nodes/")

	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node id required")
		return
	}

	node, err := h.nodes.GetByID(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	if node.AccountID != accountID {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := h.nodeService.DeregisterNode(r.Context(), nodeID); err != nil {
		h.logger.Error("delete node failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to delete node")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Heartbeat handles POST /api/v1/nodes/{id}/heartbeat
func (h *NodeHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())

	// Extract node ID from path: /api/v1/nodes/{id}/heartbeat
	path := r.URL.Path
	path = strings.TrimPrefix(path, "/api/v1/nodes/")
	nodeID := strings.TrimSuffix(path, "/heartbeat")

	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node id required")
		return
	}

	// Verify ownership
	node, err := h.nodes.GetByID(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.AccountID != accountID {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req protocol.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.NodeID = nodeID

	resp, err := h.nodeService.ProcessHeartbeat(r.Context(), req)
	if err != nil {
		h.logger.Error("heartbeat failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to process heartbeat")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func extractPathParam(path, prefix string) string {
	s := strings.TrimPrefix(path, prefix)
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// --- Internal Handlers (STUN/Relay registration) ---

type InternalHandler struct {
	stunRepo  db.STUNServerRepository
	relayRepo db.RelayServerRepository
	logger    *zap.Logger
}

func NewInternalHandler(stunRepo db.STUNServerRepository, relayRepo db.RelayServerRepository, logger *zap.Logger) *InternalHandler {
	return &InternalHandler{stunRepo: stunRepo, relayRepo: relayRepo, logger: logger}
}

// RegisterSTUN handles POST /api/v1/internal/stun/register
func (h *InternalHandler) RegisterSTUN(w http.ResponseWriter, r *http.Request) {
	var req protocol.STUNRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.stunRepo.Upsert(r.Context(), req.Address, req.Port); err != nil {
		h.logger.Error("register STUN failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to register STUN server")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// RegisterRelay handles POST /api/v1/internal/relay/register
func (h *InternalHandler) RegisterRelay(w http.ResponseWriter, r *http.Request) {
	var req protocol.RelayRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Generate a relay ID from address
	relayID := req.Address + ":" + strconv.Itoa(req.Port)

	if err := h.relayRepo.Upsert(r.Context(), relayID, req.Address, req.Port, req.Capacity); err != nil {
		h.logger.Error("register relay failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to register relay server")
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *InternalHandler) dummy() {
	// suppress unused import
	_ = api.NodeTypeRelay
}

// Needed for strconv import
func init() {}
