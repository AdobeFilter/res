package protocol

import "valhalla/common/api"

// HeartbeatRequest is sent by nodes every 15 seconds.
type HeartbeatRequest struct {
	NodeID  string      `json:"node_id"`
	Metrics api.Metrics `json:"metrics"`
}

// HeartbeatResponse contains route updates and settings pushed from control plane.
type HeartbeatResponse struct {
	UpdatedRoutes []api.Route          `json:"updated_routes,omitempty"`
	Settings      *api.AccountSettings `json:"settings,omitempty"`
	Peers         []api.PeerInfo       `json:"peers,omitempty"`
	STUNServers   []api.STUNServer     `json:"stun_servers,omitempty"`
}
