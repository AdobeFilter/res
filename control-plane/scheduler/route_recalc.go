package scheduler

import (
	"context"
	"time"

	"go.uber.org/zap"
	"valhalla/control-plane/service"
)

// RouteRecalculator periodically recalculates optimal routes.
type RouteRecalculator struct {
	routeService *service.RouteService
	interval     time.Duration
	logger       *zap.Logger
}

func NewRouteRecalculator(rs *service.RouteService, interval time.Duration, logger *zap.Logger) *RouteRecalculator {
	return &RouteRecalculator{routeService: rs, interval: interval, logger: logger}
}

// Start begins the periodic route recalculation loop.
func (r *RouteRecalculator) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info("route recalculator started", zap.Duration("interval", r.interval))

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("route recalculator stopped")
			return
		case <-ticker.C:
			if err := r.routeService.RecalculateAllRoutes(ctx); err != nil {
				r.logger.Error("route recalculation failed", zap.Error(err))
			}
		}
	}
}
