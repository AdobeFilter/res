package db

import (
	"context"

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
}

// NodeRepository handles node persistence.
type NodeRepository interface {
	Create(ctx context.Context, node *api.NodeInfo) error
	GetByID(ctx context.Context, id string) (*api.NodeInfo, error)
	GetByAccountID(ctx context.Context, accountID string) ([]*api.NodeInfo, error)
	GetOnlineByType(ctx context.Context, nodeType api.NodeType) ([]*api.NodeInfo, error)
	GetAllOnline(ctx context.Context) ([]*api.NodeInfo, error)
	UpdateEndpoint(ctx context.Context, nodeID, endpoint string, natType api.NATType) error
	UpdateStatus(ctx context.Context, nodeID string, status api.NodeStatus) error
	UpdateLastSeen(ctx context.Context, nodeID string) error
	Delete(ctx context.Context, nodeID string) error
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
	Allocate(ctx context.Context, nodeID string) (string, error)
	Release(ctx context.Context, nodeID string) error
}

// STUNServerRepository handles STUN server registration.
type STUNServerRepository interface {
	Upsert(ctx context.Context, address string, port int) error
	GetAll(ctx context.Context) ([]api.STUNServer, error)
}

// RelayServerRepository handles relay server registration.
type RelayServerRepository interface {
	Upsert(ctx context.Context, id, address string, port, capacity int) error
	GetAll(ctx context.Context) ([]api.RelayServer, error)
	GetBestAvailable(ctx context.Context) (*api.RelayServer, error)
}
