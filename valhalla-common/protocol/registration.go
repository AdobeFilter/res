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
	OS        string       `json:"os,omitempty"`
	PublicKey string       `json:"public_key"`
	DeviceID  string       `json:"device_id,omitempty"`
}

type NodeUpdateRequest struct {
	Name         string `json:"name,omitempty"`
	SharedFolder string `json:"shared_folder,omitempty"`
}

type NodeReorderRequest struct {
	NodeIDs []string `json:"node_ids"`
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
	Address   string `json:"address"`
	Port      int    `json:"port"`       // UDP port for WG hole-punch relay
	VLESSPort int    `json:"vless_port"` // TCP port for VLESS+Reality (xray subprocess)
	Capacity  int    `json:"capacity"`
}

// RelayRegisterResponse carries the Reality credentials back to the relay
// on first registration. The relay uses these to build xray's config. If the
// relay was already registered, the same credentials are returned — rotating
// them would break every client that cached the public key.
type RelayRegisterResponse struct {
	VLESSUUID          string `json:"vless_uuid"`
	RealityPrivateKey  string `json:"reality_private_key"`
	RealityPublicKey   string `json:"reality_public_key"`
	RealityShortIDs    string `json:"reality_short_ids"`
	RealitySNI         string `json:"reality_sni"`
}
