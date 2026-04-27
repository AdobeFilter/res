package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/routing"
)

type RouteService struct {
	nodes    db.NodeRepository
	metrics  db.MetricsRepository
	routes   db.RouteRepository
	relays   db.RelayServerRepository
	optimizer *routing.Optimizer
	logger   *zap.Logger
}

func NewRouteService(
	nodes db.NodeRepository,
	metrics db.MetricsRepository,
	routes db.RouteRepository,
	relays db.RelayServerRepository,
	logger *zap.Logger,
) *RouteService {
	return &RouteService{
		nodes:     nodes,
		metrics:   metrics,
		routes:    routes,
		relays:    relays,
		optimizer: routing.NewOptimizer(routing.DefaultWeights()),
		logger:    logger,
	}
}

// GetOptimalRoute calculates the best route between two nodes.
func (s *RouteService) GetOptimalRoute(ctx context.Context, srcNodeID, dstNodeID string) (*api.Route, error) {
	// Try cached route first
	cached, err := s.routes.GetOptimal(ctx, srcNodeID, dstNodeID)
	if err == nil && cached != nil {
		return cached, nil
	}

	// Calculate fresh route
	return s.calculateRoute(ctx, srcNodeID, dstNodeID)
}

// RecalculateAllRoutes recalculates routes for all online node pairs.
func (s *RouteService) RecalculateAllRoutes(ctx context.Context) error {
	nodes, err := s.nodes.GetAllOnline(ctx)
	if err != nil {
		return fmt.Errorf("get online nodes: %w", err)
	}

	allMetrics, err := s.metrics.GetAllLatest(ctx)
	if err != nil {
		return fmt.Errorf("get all metrics: %w", err)
	}

	graph := routing.BuildGraph(nodes, allMetrics, s.optimizer)

	// Calculate routes for each client → exit node pair
	var clients, exits []*api.NodeInfo
	for _, n := range nodes {
		switch n.NodeType {
		case api.NodeTypeClient:
			clients = append(clients, n)
		case api.NodeTypeExit:
			exits = append(exits, n)
		}
	}

	recalculated := 0
	for _, client := range clients {
		for _, exit := range exits {
			route, err := graph.ShortestPath(client.ID, exit.ID)
			if err != nil {
				s.logger.Debug("no path found",
					zap.String("src", client.ID),
					zap.String("dst", exit.ID),
					zap.Error(err),
				)
				continue
			}

			if err := s.routes.Upsert(ctx, route); err != nil {
				s.logger.Warn("failed to store route", zap.Error(err))
				continue
			}
			recalculated++
		}
	}

	s.logger.Info("route recalculation complete", zap.Int("routes", recalculated))
	return nil
}

// GetOptimalRouteResponse returns the full RouteResponse the client needs to
// open a tunnel: the Dijkstra route, the destination peer's WG identity, and
// — when the chosen path requires a relay — complete VLESS+Reality
// credentials for that relay. Direct paths leave Relay nil.
//
// If Dijkstra can't connect src → dst (the relay-servers registry lives
// outside the node graph, so a pure-client↔client pair with no public
// endpoints will legitimately fail here), we fall back to a relay route
// using GetBestAvailable. That's the DPI-bypass case the whole L3-mesh
// over VLESS is built for.
func (s *RouteService) GetOptimalRouteResponse(ctx context.Context, srcNodeID, dstNodeID string) (*protocol.RouteResponse, error) {
	dst, err := s.nodes.GetByID(ctx, dstNodeID)
	if err != nil {
		return nil, fmt.Errorf("lookup dst node: %w", err)
	}
	if dst == nil {
		return nil, api.ErrNoRouteAvailable
	}

	route, routeErr := s.GetOptimalRoute(ctx, srcNodeID, dstNodeID)
	connType := api.ConnectionRelay
	if routeErr == nil && route != nil {
		connType = route.ConnectionType
	} else if !errors.Is(routeErr, api.ErrNoRouteAvailable) && routeErr != nil {
		return nil, routeErr
	}

	resp := &protocol.RouteResponse{
		ConnectionType: connType,
		DstPeer: api.PeerInfo{
			PublicKey:  dst.PublicKey,
			Endpoint:   dst.Endpoint,
			InternalIP: dst.InternalIP,
			NodeType:   dst.NodeType,
		},
	}
	if route != nil {
		resp.Route = *route
	} else {
		// Synthesize a minimal route record so the client still sees a
		// coherent path even when Dijkstra bailed.
		resp.Route = api.Route{
			SrcNodeID:      srcNodeID,
			DstNodeID:      dstNodeID,
			Path:           []string{srcNodeID, dstNodeID},
			ConnectionType: api.ConnectionRelay,
		}
	}

	if resp.ConnectionType == api.ConnectionRelay {
		relay, err := s.relays.GetBestAvailable(ctx)
		if err != nil {
			return nil, fmt.Errorf("no relay available: %w", err)
		}
		resp.Relay = &protocol.RelayEndpoint{
			Address:          relay.Address,
			VLESSPort:        relay.VLESSPort,
			VLESSUUID:        relay.VLESSUUID,
			RealityPublicKey: relay.RealityPublicKey,
			RealitySNI:       relay.RealitySNI,
			RealityShortID:   firstShortID(relay.RealityShortIDs),
		}
		resp.Route.RelayNodeID = relay.ID
	}

	return resp, nil
}

// GetRelayEndpoint returns the best relay's credentials so a client can
// configure its xray mesh-chain at VpnService start without needing to
// pretend a target peer first. Same data the GetOptimalRouteResponse
// embeds when ConnectionType is relay, just without the dst_peer half.
func (s *RouteService) GetRelayEndpoint(ctx context.Context) (*protocol.RelayEndpoint, error) {
	relay, err := s.relays.GetBestAvailable(ctx)
	if err != nil {
		return nil, fmt.Errorf("no relay available: %w", err)
	}
	return &protocol.RelayEndpoint{
		Address:          relay.Address,
		VLESSPort:        relay.VLESSPort,
		VLESSUUID:        relay.VLESSUUID,
		RealityPublicKey: relay.RealityPublicKey,
		RealitySNI:       relay.RealitySNI,
		RealityShortID:   firstShortID(relay.RealityShortIDs),
	}, nil
}

// firstShortID picks the first entry from a CSV list (the form in which the
// DB stores Reality short IDs). Empty list → empty string, which xray treats
// as "no short id" and still accepts.
func firstShortID(csv string) string {
	if csv == "" {
		return ""
	}
	if idx := strings.Index(csv, ","); idx >= 0 {
		return strings.TrimSpace(csv[:idx])
	}
	return strings.TrimSpace(csv)
}

func (s *RouteService) calculateRoute(ctx context.Context, srcNodeID, dstNodeID string) (*api.Route, error) {
	nodes, err := s.nodes.GetAllOnline(ctx)
	if err != nil {
		return nil, fmt.Errorf("get online nodes: %w", err)
	}

	allMetrics, err := s.metrics.GetAllLatest(ctx)
	if err != nil {
		return nil, fmt.Errorf("get metrics: %w", err)
	}

	graph := routing.BuildGraph(nodes, allMetrics, s.optimizer)
	route, err := graph.ShortestPath(srcNodeID, dstNodeID)
	if err != nil {
		return nil, api.ErrNoRouteAvailable
	}

	if err := s.routes.Upsert(ctx, route); err != nil {
		s.logger.Warn("failed to cache route", zap.Error(err))
	}

	return route, nil
}
