package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/events"
	"valhalla/control-plane/middleware"
	"valhalla/control-plane/service"
)

// maxHeartbeatWait caps the long-poll hold time. Bounded so a stuck client
// doesn't tie a goroutine forever; also fits comfortably under the http
// server's WriteTimeout (180s).
const maxHeartbeatWait = 30 * time.Second

type NodeHandler struct {
	nodeService *service.NodeService
	nodes       db.NodeRepository
	events      *events.Broker
	logger      *zap.Logger
}

func NewNodeHandler(nodeService *service.NodeService, nodes db.NodeRepository, broker *events.Broker, logger *zap.Logger) *NodeHandler {
	return &NodeHandler{nodeService: nodeService, nodes: nodes, events: broker, logger: logger}
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
		switch {
		case errors.Is(err, api.ErrDeviceAlreadyLinked):
			writeError(w, http.StatusConflict, "this device is already linked to another account")
			return
		case errors.Is(err, api.ErrDeviceLimitReached):
			writeError(w, http.StatusConflict, "device limit reached (max 20 per account)")
			return
		}
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

// Update handles PUT /api/v1/nodes/{id}
func (h *NodeHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var req protocol.NodeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != "" {
		if err := h.nodes.UpdateName(r.Context(), nodeID, req.Name); err != nil {
			h.logger.Error("update node name failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to update node")
			return
		}
	}

	if req.SharedFolder != "" {
		if err := h.nodes.UpdateSharedFolder(r.Context(), nodeID, req.SharedFolder); err != nil {
			h.logger.Error("update shared folder failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to update node")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// Reorder handles POST /api/v1/nodes/reorder
func (h *NodeHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req protocol.NodeReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for i, nodeID := range req.NodeIDs {
		node, err := h.nodes.GetByID(r.Context(), nodeID)
		if err != nil || node.AccountID != accountID {
			continue
		}
		if err := h.nodes.UpdateSortOrder(r.Context(), nodeID, i); err != nil {
			h.logger.Error("update sort order failed", zap.Error(err))
		}
	}

	w.WriteHeader(http.StatusNoContent)
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

	// Our own write changed last_seen / endpoint / lan_ip — wake other
	// devices on this account that are sitting on a long-poll heartbeat.
	h.events.Publish(node.AccountID)

	// Long-poll: if the client asked to wait, hold the response until
	// something account-scoped changes (settings update, peer added,
	// peer's heartbeat refreshed) or the wait budget elapses. Without
	// ?wait= the handler returns immediately — old clients unchanged.
	if wait := parseHeartbeatWait(r.URL.Query().Get("wait")); wait > 0 {
		ch, unsub := h.events.Subscribe(node.AccountID)
		defer unsub()
		select {
		case <-ch:
			if refreshed, rerr := h.nodeService.BuildHeartbeatResponse(r.Context(), nodeID); rerr == nil {
				resp = refreshed
			}
		case <-time.After(wait):
			// timeout — return what ProcessHeartbeat built initially
		case <-r.Context().Done():
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// parseHeartbeatWait clamps the ?wait=<seconds> param to [0, maxHeartbeatWait].
func parseHeartbeatWait(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > maxHeartbeatWait {
		d = maxHeartbeatWait
	}
	return d
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
	stunRepo          db.STUNServerRepository
	relayRepo         db.RelayServerRepository
	relayAllowlistPath string
	logger            *zap.Logger
}

func NewInternalHandler(stunRepo db.STUNServerRepository, relayRepo db.RelayServerRepository, relayAllowlistPath string, logger *zap.Logger) *InternalHandler {
	return &InternalHandler{
		stunRepo:          stunRepo,
		relayRepo:         relayRepo,
		relayAllowlistPath: relayAllowlistPath,
		logger:            logger,
	}
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

// RegisterRelay handles POST /api/v1/internal/relay/register.
//
// On first registration the control-plane mints a VLESS UUID + Reality
// keypair + SNI for this relay and stores them. Subsequent calls from the
// same (address, port) pair return the same credentials — the relay restores
// its xray config from them instead of re-negotiating crypto at every boot.
func (h *InternalHandler) RegisterRelay(w http.ResponseWriter, r *http.Request) {
	var req protocol.RelayRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := checkRelayAllowed(r, req.Address, h.relayAllowlistPath); err != nil {
		h.logger.Warn("relay registration rejected",
			zap.String("declared", req.Address),
			zap.String("remote", r.RemoteAddr),
			zap.Error(err))
		writeError(w, http.StatusForbidden, "relay not allowed")
		return
	}

	// Default vless_port if not provided (old relay binaries that don't know
	// about it yet still register a UDP-only relay; xray-subprocess is
	// optional from the control-plane's perspective).
	vlessPort := req.VLESSPort
	if vlessPort == 0 {
		vlessPort = 443
	}

	// Cap self-declared capacity so a misconfigured or hostile relay can't
	// monopolise GetBestAvailable by claiming a huge slot count. Real ranking
	// by measured throughput/RTT belongs in heartbeat metrics; until then,
	// this is the simplest mitigation.
	capacity := req.Capacity
	if capacity > maxRelayCapacity {
		capacity = maxRelayCapacity
	}

	creds, err := h.relayRepo.UpsertWithCredentials(
		r.Context(), req.Address, req.Port, vlessPort, capacity,
	)
	if err != nil {
		h.logger.Error("register relay failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to register relay server")
		return
	}

	writeJSON(w, http.StatusOK, protocol.RelayRegisterResponse{
		VLESSUUID:         creds.VLESSUUID,
		RealityPrivateKey: creds.RealityPrivateKey,
		RealityPublicKey:  creds.RealityPublicKey,
		RealityShortIDs:   creds.RealityShortIDs,
		RealitySNI:        creds.RealitySNI,
	})
}

func (h *InternalHandler) dummy() {
	// suppress unused import
	_ = api.NodeTypeRelay
}

// Needed for strconv import
func init() {}
