package protocol

import "valhalla/common/api"

// --- Auth ---

type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
}

// --- Node Registration ---

type NodeRegisterRequest struct {
	Name      string       `json:"name"`
	NodeType  api.NodeType `json:"node_type"`
	PublicKey string       `json:"public_key"`
}

type NodeRegisterResponse struct {
	NodeID     string         `json:"node_id"`
	InternalIP string         `json:"internal_ip"`
	Peers      []api.PeerInfo `json:"peers"`
}

// --- Internal Registration (STUN/Relay self-registration) ---

type STUNRegisterRequest struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type RelayRegisterRequest struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Capacity int    `json:"capacity"`
}
