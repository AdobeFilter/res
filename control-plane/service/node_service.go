package service

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
)

type NodeService struct {
	nodes            db.NodeRepository
	metrics          db.MetricsRepository
	settings         db.AccountSettingsRepository
	stunServers      db.STUNServerRepository
	ipAlloc          db.IPAllocator
	routes           db.RouteRepository
	antifraudEnabled bool
	logger           *zap.Logger
}

func NewNodeService(
	nodes db.NodeRepository,
	metrics db.MetricsRepository,
	settings db.AccountSettingsRepository,
	stunServers db.STUNServerRepository,
	ipAlloc db.IPAllocator,
	routes db.RouteRepository,
	antifraudEnabled bool,
	logger *zap.Logger,
) *NodeService {
	return &NodeService{
		nodes:            nodes,
		metrics:          metrics,
		settings:         settings,
		stunServers:      stunServers,
		ipAlloc:          ipAlloc,
		routes:           routes,
		antifraudEnabled: antifraudEnabled,
		logger:           logger,
	}
}

// maxDevicesPerAccount caps how many devices (nodes with device_id) one
// account can have linked. Prevents account-sharing at scale and bounds
// fan-out on free tier.
const maxDevicesPerAccount = 20

// RegisterNode creates, re-registers, or migrates a node. Invariant: there
// is at most one row in nodes per device_id (ghost rows from earlier test
// runs are cleaned up on every register).
//
// With antifraudEnabled=true:
//   - device_id linked to another account → ErrDeviceAlreadyLinked (409)
//   - at most maxDevicesPerAccount per account → ErrDeviceLimitReached
//
// With antifraudEnabled=false (test/dogfooding mode):
//   - cross-account register silently MIGRATES the existing row (UPDATE
//     account_id, name, pubkey, status). The phone "follows" the user.
//   - any other ghost rows for this device_id are deleted.
//
// In both modes, same-account re-register is idempotent (handles app
// reinstall — ANDROID_ID survives on Android 8+).
func (s *NodeService) RegisterNode(ctx context.Context, accountID string, req protocol.NodeRegisterRequest) (*protocol.NodeRegisterResponse, error) {
	if req.DeviceID != "" {
		existing, _ := s.nodes.FindByDeviceIDGlobal(ctx, req.DeviceID)
		if existing != nil {
			crossAccount := existing.AccountID != accountID
			if crossAccount && s.antifraudEnabled {
				s.logger.Warn("device_id collision across accounts",
					zap.String("device_id", req.DeviceID),
					zap.String("owner_account", existing.AccountID),
					zap.String("attempting_account", accountID))
				return nil, api.ErrDeviceAlreadyLinked
			}

			// Test-mode migration OR same-account re-register: rewrite the
			// row in place. UpdateReregister also sets account_id.
			existing.AccountID = accountID
			existing.Name = req.Name
			existing.PublicKey = req.PublicKey
			existing.OS = req.OS
			existing.Status = api.NodeStatusOnline
			if err := s.nodes.UpdateReregister(ctx, existing); err != nil {
				s.logger.Warn("failed to update existing node", zap.Error(err))
				return nil, fmt.Errorf("update node: %w", err)
			}

			// Sweep ghost rows for the same device_id (only possible in test
			// mode where the global unique index was dropped). Quiet best-effort.
			if removed, err := s.nodes.DeleteOtherByDeviceID(ctx, req.DeviceID, existing.ID); err == nil && removed > 0 {
				s.logger.Info("cleaned up ghost device rows",
					zap.String("device_id", req.DeviceID),
					zap.Int64("removed", removed))
			}

			if crossAccount {
				s.logger.Info("migrated device to new account",
					zap.String("node_id", existing.ID),
					zap.String("device_id", req.DeviceID),
					zap.String("new_account", accountID))
			} else {
				s.logger.Info("re-registered existing node",
					zap.String("node_id", existing.ID),
					zap.String("device_id", req.DeviceID))
			}

			peers, _ := s.getPeers(ctx, existing.ID)
			return &protocol.NodeRegisterResponse{
				NodeID:     existing.ID,
				InternalIP: existing.InternalIP,
				Peers:      peers,
			}, nil
		}

		// No prior row for this device anywhere → fresh registration, enforce
		// the per-account device limit.
		count, err := s.nodes.CountDevicesByAccount(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("count devices: %w", err)
		}
		if count >= maxDevicesPerAccount {
			return nil, api.ErrDeviceLimitReached
		}
	}

	node := &api.NodeInfo{
		AccountID: accountID,
		Name:      req.Name,
		NodeType:  req.NodeType,
		OS:        req.OS,
		DeviceID:  req.DeviceID,
		PublicKey: req.PublicKey,
		Status:    api.NodeStatusOnline,
	}

	// Find available mesh IP
	ip, err := s.ipAlloc.FindAvailable(ctx)
	if err != nil {
		return nil, fmt.Errorf("allocate IP: %w", err)
	}
	node.InternalIP = ip

	// Create node in DB (generates node.ID)
	if err := s.nodes.Create(ctx, node); err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}

	// Record IP allocation with real node ID
	if err := s.ipAlloc.AssignIP(ctx, ip, node.ID); err != nil {
		s.logger.Warn("failed to record IP allocation", zap.Error(err))
	}

	// Get peers for this node (other online nodes in the mesh)
	peers, err := s.getPeers(ctx, node.ID)
	if err != nil {
		s.logger.Warn("failed to get peers", zap.Error(err))
	}

	return &protocol.NodeRegisterResponse{
		NodeID:     node.ID,
		InternalIP: node.InternalIP,
		Peers:      peers,
	}, nil
}

// DeregisterNode removes a node and releases its IP.
func (s *NodeService) DeregisterNode(ctx context.Context, nodeID string) error {
	if err := s.ipAlloc.Release(ctx, nodeID); err != nil {
		s.logger.Warn("failed to release IP", zap.Error(err))
	}
	return s.nodes.Delete(ctx, nodeID)
}

// ProcessHeartbeat updates node status, stores metrics, and returns route/settings updates.
func (s *NodeService) ProcessHeartbeat(ctx context.Context, req protocol.HeartbeatRequest) (*protocol.HeartbeatResponse, error) {
	// Update last seen
	if err := s.nodes.UpdateLastSeen(ctx, req.NodeID); err != nil {
		return nil, fmt.Errorf("update last seen: %w", err)
	}

	// Update endpoint if provided
	if req.Endpoint != "" {
		if err := s.nodes.UpdateEndpoint(ctx, req.NodeID, req.Endpoint, api.NATType("")); err != nil {
			s.logger.Warn("failed to update endpoint", zap.Error(err))
		}
	}

	// Update LAN IP if provided
	if req.LanIP != "" {
		if err := s.nodes.UpdateLanIP(ctx, req.NodeID, req.LanIP); err != nil {
			s.logger.Warn("failed to update lan_ip", zap.Error(err))
		}
	}

	// Store metrics
	req.Metrics.NodeID = req.NodeID
	if err := s.metrics.Insert(ctx, &req.Metrics); err != nil {
		s.logger.Warn("failed to insert metrics", zap.Error(err))
	}

	return s.BuildHeartbeatResponse(ctx, req.NodeID)
}

// BuildHeartbeatResponse reads (no writes) the current settings, peer list,
// and STUN servers for the given node's account. Used both as the tail of
// ProcessHeartbeat and as the re-fetch on a long-poll wake-up.
func (s *NodeService) BuildHeartbeatResponse(ctx context.Context, nodeID string) (*protocol.HeartbeatResponse, error) {
	node, err := s.nodes.GetByID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}

	resp := &protocol.HeartbeatResponse{}

	if settings, err := s.settings.Get(ctx, node.AccountID); err == nil {
		resp.Settings = settings
	}
	if peers, err := s.getPeers(ctx, nodeID); err == nil {
		resp.Peers = peers
	}
	if stunServers, err := s.stunServers.GetAll(ctx); err == nil {
		resp.STUNServers = stunServers
	}
	return resp, nil
}

func (s *NodeService) getPeers(ctx context.Context, excludeNodeID string) ([]api.PeerInfo, error) {
	nodes, err := s.nodes.GetAllOnline(ctx)
	if err != nil {
		return nil, err
	}

	var peers []api.PeerInfo
	for _, n := range nodes {
		if n.ID == excludeNodeID {
			continue
		}
		peers = append(peers, api.PeerInfo{
			PublicKey:  n.PublicKey,
			Endpoint:   n.Endpoint,
			InternalIP: n.InternalIP,
			NodeType:   n.NodeType,
		})
	}
	return peers, nil
}
