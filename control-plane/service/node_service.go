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
	nodes       db.NodeRepository
	metrics     db.MetricsRepository
	settings    db.AccountSettingsRepository
	stunServers db.STUNServerRepository
	ipAlloc     db.IPAllocator
	routes      db.RouteRepository
	logger      *zap.Logger
}

func NewNodeService(
	nodes db.NodeRepository,
	metrics db.MetricsRepository,
	settings db.AccountSettingsRepository,
	stunServers db.STUNServerRepository,
	ipAlloc db.IPAllocator,
	routes db.RouteRepository,
	logger *zap.Logger,
) *NodeService {
	return &NodeService{
		nodes:       nodes,
		metrics:     metrics,
		settings:    settings,
		stunServers: stunServers,
		ipAlloc:     ipAlloc,
		routes:      routes,
		logger:      logger,
	}
}

// RegisterNode creates a new node, allocates an internal IP, and returns peer list.
func (s *NodeService) RegisterNode(ctx context.Context, accountID string, req protocol.NodeRegisterRequest) (*protocol.NodeRegisterResponse, error) {
	node := &api.NodeInfo{
		AccountID: accountID,
		Name:      req.Name,
		NodeType:  req.NodeType,
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

	// Store metrics
	req.Metrics.NodeID = req.NodeID
	if err := s.metrics.Insert(ctx, &req.Metrics); err != nil {
		s.logger.Warn("failed to insert metrics", zap.Error(err))
	}

	// Get node to find account
	node, err := s.nodes.GetByID(ctx, req.NodeID)
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}

	resp := &protocol.HeartbeatResponse{}

	// Include account settings
	settings, err := s.settings.Get(ctx, node.AccountID)
	if err == nil {
		resp.Settings = settings
	}

	// Include updated peers
	peers, err := s.getPeers(ctx, req.NodeID)
	if err == nil {
		resp.Peers = peers
	}

	// Include STUN servers
	stunServers, err := s.stunServers.GetAll(ctx)
	if err == nil {
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
