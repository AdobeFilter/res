package service

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"valhalla/common/api"
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
