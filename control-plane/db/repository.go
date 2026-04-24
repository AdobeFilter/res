package db

import (
	"context"
	"time"

	"valhalla/common/api"
)

// AccountRepository handles account persistence.
type AccountRepository interface {
	Create(ctx context.Context, email, passwordHash string) (*api.Account, error)
	GetByEmail(ctx context.Context, email string) (*api.Account, error)
	GetByID(ctx context.Context, id string) (*api.Account, error)
}

// AccountSettingsRepository handles account settings.
type AccountSettingsRepository interface {
	Get(ctx context.Context, accountID string) (*api.AccountSettings, error)
	Upsert(ctx context.Context, accountID string, vlessEnabled bool) (*api.AccountSettings, error)
	SetExitNode(ctx context.Context, accountID string, exitNodeID *string) (*api.AccountSettings, error)
	SetExitNodes(ctx context.Context, accountID string, nodes []api.ExitNodeConfig) (*api.AccountSettings, error)
	SetRoutingRules(ctx context.Context, accountID string, rules string) (*api.AccountSettings, error)
	SetFragmentEnabled(ctx context.Context, accountID string, enabled bool) (*api.AccountSettings, error)
	SetBlockAdsEnabled(ctx context.Context, accountID string, enabled bool) (*api.AccountSettings, error)
}

// NodeRepository handles node persistence.
type NodeRepository interface {
	Create(ctx context.Context, node *api.NodeInfo) error
	GetByID(ctx context.Context, id string) (*api.NodeInfo, error)
	GetByDeviceID(ctx context.Context, accountID, deviceID string) (*api.NodeInfo, error)
	GetByAccountID(ctx context.Context, accountID string) ([]*api.NodeInfo, error)
	UpdateReregister(ctx context.Context, node *api.NodeInfo) error
	GetOnlineByType(ctx context.Context, nodeType api.NodeType) ([]*api.NodeInfo, error)
	GetAllOnline(ctx context.Context) ([]*api.NodeInfo, error)
	UpdateEndpoint(ctx context.Context, nodeID, endpoint string, natType api.NATType) error
	UpdateStatus(ctx context.Context, nodeID string, status api.NodeStatus) error
	UpdateName(ctx context.Context, nodeID, name string) error
	UpdateLanIP(ctx context.Context, nodeID, lanIP string) error
	UpdateSortOrder(ctx context.Context, nodeID string, sortOrder int) error
	UpdateSharedFolder(ctx context.Context, nodeID, folder string) error
	UpdateLastSeen(ctx context.Context, nodeID string) error
	Delete(ctx context.Context, nodeID string) error
	// DeleteStaleBefore removes any node whose last_seen is older than cutoff
	// (or whose last_seen is NULL and whose created_at is older than cutoff).
	// Returns the number of rows deleted.
	DeleteStaleBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// MetricsRepository handles node metrics.
type MetricsRepository interface {
	Insert(ctx context.Context, metrics *api.Metrics) error
	GetLatest(ctx context.Context, nodeID string) (*api.Metrics, error)
	GetAllLatest(ctx context.Context) (map[string]*api.Metrics, error)
}

// RouteRepository handles routes.
type RouteRepository interface {
	Upsert(ctx context.Context, route *api.Route) error
	GetOptimal(ctx context.Context, srcNodeID, dstNodeID string) (*api.Route, error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// IPAllocator handles mesh IP allocation.
type IPAllocator interface {
	FindAvailable(ctx context.Context) (string, error)
	Allocate(ctx context.Context, nodeID string) (string, error)
	AssignIP(ctx context.Context, ip string, nodeID string) error
	Release(ctx context.Context, nodeID string) error
}

// STUNServerRepository handles STUN server registration.
type STUNServerRepository interface {
	Upsert(ctx context.Context, address string, port int) error
	GetAll(ctx context.Context) ([]api.STUNServer, error)
}

// RelayCredentials are the Reality keypair + VLESS UUID + SNI we hand back
// to a relay when it registers. Stored in the DB so the same credentials are
// returned on every restart (rotating would break clients that cached pbk).
type RelayCredentials struct {
	VLESSUUID         string
	RealityPrivateKey string
	RealityPublicKey  string
	RealityShortIDs   string // comma-separated
	RealitySNI        string
}

// RelayServerRepository handles relay server registration.
type RelayServerRepository interface {
	// UpsertWithCredentials registers a relay and returns its Reality
	// credentials — generating and storing a fresh set on first call for a
	// given (address, port) pair, returning the stored ones on subsequent
	// calls. vlessPort is the TCP port where xray will listen.
	UpsertWithCredentials(
		ctx context.Context,
		id, address string,
		port, vlessPort, capacity int,
	) (*RelayCredentials, error)

	GetAll(ctx context.Context) ([]api.RelayServer, error)
	GetBestAvailable(ctx context.Context) (*api.RelayServer, error)
}
