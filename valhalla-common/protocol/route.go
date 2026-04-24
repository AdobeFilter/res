package protocol

import "valhalla/common/api"

// RouteRequest queries the optimal route between two nodes.
type RouteRequest struct {
	FromNodeID string `json:"from"`
	ToNodeID   string `json:"to"`
}

// RouteResponse contains the optimal route and connection instructions.
type RouteResponse struct {
	Route          api.Route      `json:"route"`
	ConnectionType api.ConnectionType `json:"connection_type"`
	RelayEndpoint  string         `json:"relay_endpoint,omitempty"`
	PeerEndpoint   string         `json:"peer_endpoint,omitempty"`
}

// STUNServersResponse returns available STUN servers.
type STUNServersResponse struct {
	Servers []api.STUNServer `json:"servers"`
}

// DevicesResponse returns devices belonging to an account.
type DevicesResponse struct {
	Devices []api.Device `json:"devices"`
}

// SettingsResponse returns account settings.
type SettingsResponse struct {
	Settings api.AccountSettings `json:"settings"`
}

// UpdateSettingsRequest updates account-level settings. Every field is a
// pointer so the caller can push a partial patch — only non-nil fields are
// written. ExitNodes is a whole-list replacement (the client always sends the
// full list after any add/remove/edit).
type UpdateSettingsRequest struct {
	VLESSEnabled    *bool                `json:"vless_enabled,omitempty"`
	ExitNodeID      *string              `json:"exit_node_id,omitempty"`
	ExitNodes       *[]api.ExitNodeConfig `json:"exit_nodes,omitempty"`
	RoutingRules    *string              `json:"routing_rules,omitempty"`
	FragmentEnabled *bool                `json:"fragment_enabled,omitempty"`
	BlockAdsEnabled *bool                `json:"block_ads_enabled,omitempty"`
}
