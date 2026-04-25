package protocol

import "valhalla/common/api"

// RouteRequest queries the optimal route between two nodes.
type RouteRequest struct {
	FromNodeID string `json:"from"`
	ToNodeID   string `json:"to"`
}

// RelayEndpoint is everything a client needs to build a VLESS+Reality xray
// outbound config pointing at a specific relay. Fields mirror the xray
// realitySettings schema 1:1 so the client can template its config without
// any translation. Short ID is a single value (the first one the relay
// advertises) — xray accepts it verbatim; the relay-side list is a superset.
type RelayEndpoint struct {
	Address          string `json:"address"`
	VLESSPort        int    `json:"vless_port"`
	VLESSUUID        string `json:"vless_uuid"`
	RealityPublicKey string `json:"reality_public_key"`
	RealitySNI       string `json:"reality_sni"`
	RealityShortID   string `json:"reality_short_id"`
}

// RouteResponse is returned by GET /api/v1/routes/optimal. It carries the
// Dijkstra result plus everything the client needs to actually open the
// tunnel: the destination peer's WG identity (pubkey + mesh IP) and, when
// ConnectionType == relay, the full VLESS+Reality credentials for the
// chosen relay. ConnectionType == direct leaves Relay nil and lets the
// client dial the peer's endpoint straight.
type RouteResponse struct {
	Route          api.Route          `json:"route"`
	ConnectionType api.ConnectionType `json:"connection_type"`
	DstPeer        api.PeerInfo       `json:"dst_peer"`
	Relay          *RelayEndpoint     `json:"relay,omitempty"`
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
	VLESSEnabled    *bool                 `json:"vless_enabled,omitempty"`
	ExitNodeID      *string               `json:"exit_node_id,omitempty"`
	ExitNodes       *[]api.ExitNodeConfig `json:"exit_nodes,omitempty"`
	RoutingRules    *string               `json:"routing_rules,omitempty"`
	FragmentEnabled *bool                 `json:"fragment_enabled,omitempty"`
	BlockAdsEnabled *bool                 `json:"block_ads_enabled,omitempty"`
}
