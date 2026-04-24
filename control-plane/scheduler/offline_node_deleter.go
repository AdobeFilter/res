package scheduler

import (
	"context"
	"time"

	"go.uber.org/zap"
	"valhalla/control-plane/db"
)

// OfflineNodeDeleter permanently removes nodes that have been offline for
// longer than the configured retention window. Runs on a ticker; cheap single
// DELETE query so no need to batch.
type OfflineNodeDeleter struct {
	nodes     db.NodeRepository
	retention time.Duration
	interval  time.Duration
	logger    *zap.Logger
}

func NewOfflineNodeDeleter(nodes db.NodeRepository, retention, interval time.Duration, logger *zap.Logger) *OfflineNodeDeleter {
	return &OfflineNodeDeleter{
		nodes:     nodes,
		retention: retention,
		interval:  interval,
		logger:    logger,
	}
}

func (d *OfflineNodeDeleter) Start(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.logger.Info("offline node deleter started",
		zap.Duration("retention", d.retention),
		zap.Duration("interval", d.interval),
	)

	// Run once at startup so a just-restarted control-plane cleans up
	// anything that expired while it was down.
	d.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("offline node deleter stopped")
			return
		case <-ticker.C:
			d.sweep(ctx)
		}
	}
}

func (d *OfflineNodeDeleter) sweep(ctx context.Context) {
	cutoff := time.Now().Add(-d.retention)
	n, err := d.nodes.DeleteStaleBefore(ctx, cutoff)
	if err != nil {
		d.logger.Error("delete stale nodes failed", zap.Error(err))
		return
	}
	if n > 0 {
		d.logger.Info("deleted stale nodes", zap.Int64("count", n), zap.Time("cutoff", cutoff))
	}
}
