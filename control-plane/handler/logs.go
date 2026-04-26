package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"valhalla/control-plane/middleware"
)

// ConnectionLogHandler accepts client-side connection events (mesh start,
// xray start, errors, file-fetch outcomes...) and appends them to a
// per-account JSONL file under /var/log/valhalla/. The file is the
// authoritative debug trail for "why did this device fail to connect" —
// the app stays clean of in-process log buffers.
type ConnectionLogHandler struct {
	dir    string
	logger *zap.Logger

	mu    sync.Mutex
	files map[string]*os.File
}

func NewConnectionLogHandler(dir string, logger *zap.Logger) *ConnectionLogHandler {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("conn-log dir mkdir failed", zap.String("dir", dir), zap.Error(err))
	}
	return &ConnectionLogHandler{
		dir:    dir,
		logger: logger,
		files:  make(map[string]*os.File),
	}
}

type connLogEntry struct {
	Timestamp string `json:"ts"`
	AccountID string `json:"account_id"`
	NodeID    string `json:"node_id"`
	PeerID    string `json:"peer_id,omitempty"`
	Event     string `json:"event"`
	Mode      string `json:"mode,omitempty"`    // "wg-wg", "xray-wg", "wg-xray", "xray-xray", "mesh-relay"
	Detail    string `json:"detail,omitempty"`
}

type connLogRequest struct {
	NodeID string `json:"node_id"`
	PeerID string `json:"peer_id,omitempty"`
	Event  string `json:"event"`
	Mode   string `json:"mode,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (h *ConnectionLogHandler) Append(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "missing account")
		return
	}
	var req connLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.NodeID == "" || req.Event == "" {
		writeError(w, http.StatusBadRequest, "node_id and event required")
		return
	}

	entry := connLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		AccountID: accountID,
		NodeID:    req.NodeID,
		PeerID:    req.PeerID,
		Event:     req.Event,
		Mode:      req.Mode,
		Detail:    req.Detail,
	}

	line, _ := json.Marshal(&entry)
	if err := h.appendLine(accountID, line); err != nil {
		h.logger.Warn("conn-log write failed", zap.String("account", accountID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}

	// Also surface in the journal so the operator can `journalctl -f`
	// without picking the right file. account_id is the field to grep on.
	h.logger.Info("conn-log",
		zap.String("account_id", accountID),
		zap.String("node_id", req.NodeID),
		zap.String("peer_id", req.PeerID),
		zap.String("event", req.Event),
		zap.String("mode", req.Mode),
		zap.String("detail", req.Detail))

	w.WriteHeader(http.StatusNoContent)
}

func (h *ConnectionLogHandler) appendLine(accountID string, line []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, ok := h.files[accountID]
	if !ok {
		path := filepath.Join(h.dir, "conn-"+accountID+".log")
		var err error
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		h.files[accountID] = f
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}
