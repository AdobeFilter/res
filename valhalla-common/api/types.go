package api

import (
	"time"
)

// NodeType represents the type of node in the mesh network.
type NodeType string

const (
	NodeTypeClient NodeType = "client"
	NodeTypeRelay  NodeType = "relay"
	NodeTypeExit   NodeType = "exit"
)

// NodeStatus represents the current status of a node.
type NodeStatus string

const (
	NodeStatusOnline   NodeStatus = "online"
	NodeStatusOffline  NodeStatus = "offline"
	NodeStatusDegraded NodeStatus = "degraded"
)

// ConnectionType represents how two nodes are connected.
type ConnectionType string

const (
	ConnectionDirect ConnectionType = "direct"
	ConnectionSTUN   ConnectionType = "stun"
	ConnectionRelay  ConnectionType = "relay"
)

// NATType represents the type of NAT the node is behind.
type NATType string

const (
	NATNone       NATType = "none"
	NATFullCone   NATType = "full-cone"
	NATRestricted NATType = "restricted"
	NATPortRestr  NATType = "port-restricted"
	NATSymmetric  NATType = "symmetric"
)

// Account represents a user account.
type Account struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AccountSettings holds account-level settings synced across all devices.
type AccountSettings struct {
	AccountID    string    `json:"account_id"`
	VLESSEnabled bool      `json:"vless_enabled"`
	ExitNodeID   string    `json:"exit_node_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NodeInfo represents a node in the mesh network.
type NodeInfo struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	Name       string     `json:"name"`
	NodeType   NodeType   `json:"node_type"`
	OS         string     `json:"os,omitempty"`
	PublicKey  string     `json:"public_key"`
	Endpoint   string     `json:"endpoint,omitempty"`
	NATType    NATType    `json:"nat_type,omitempty"`
	InternalIP string     `json:"internal_ip,omitempty"`
	Status     NodeStatus `json:"status"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// PeerInfo is a subset of NodeInfo shared with other nodes for WireGuard configuration.
type PeerInfo struct {
	PublicKey  string `json:"public_key"`
	Endpoint   string `json:"endpoint,omitempty"`
	InternalIP string `json:"internal_ip"`
	NodeType   NodeType `json:"node_type"`
}

// Metrics holds real-time metrics for a node.
type Metrics struct {
	NodeID       string    `json:"node_id"`
	RTTMs        float64   `json:"rtt_ms"`
	BandwidthMbps float64  `json:"bandwidth_mbps"`
	CPUPercent   float64   `json:"cpu_percent"`
	ActiveConns  int       `json:"active_conns"`
	PacketLoss   float64   `json:"packet_loss"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// Route represents a calculated route between two nodes.
type Route struct {
	ID             int64          `json:"id"`
	SrcNodeID      string         `json:"src_node_id"`
	DstNodeID      string         `json:"dst_node_id"`
	Path           []string       `json:"path"`
	Cost           float64        `json:"cost"`
	ConnectionType ConnectionType `json:"connection_type"`
	RelayNodeID    string         `json:"relay_node_id,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

// Device represents a device belonging to an account (for device list display).
type Device struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	NodeType   NodeType    `json:"node_type"`
	Status     NodeStatus  `json:"status"`
	InternalIP string      `json:"internal_ip"`
	LastSeen   *time.Time  `json:"last_seen,omitempty"`
}

// STUNServer represents a registered STUN server.
type STUNServer struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// RelayServer represents a registered relay server.
type RelayServer struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Capacity int    `json:"capacity"`
}
