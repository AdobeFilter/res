package scheduler

import (
	"context"
	"time"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/control-plane/db"
	"valhalla/control-plane/events"
)

// StaleNodeCleaner marks nodes as offline if they haven't sent a heartbeat.
type StaleNodeCleaner struct {
	nodes    db.NodeRepository
	events   *events.Broker
	timeout  time.Duration
	interval time.Duration
	logger   *zap.Logger
}

func NewStaleNodeCleaner(nodes db.NodeRepository, broker *events.Broker, timeout, interval time.Duration, logger *zap.Logger) *StaleNodeCleaner {
	return &StaleNodeCleaner{
		nodes:    nodes,
		events:   broker,
		timeout:  timeout,
		interval: interval,
		logger:   logger,
	}
}

// Start begins the periodic stale node cleanup loop.
func (c *StaleNodeCleaner) Start(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.logger.Info("stale node cleaner started", zap.Duration("timeout", c.timeout))

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("stale node cleaner stopped")
			return
		case <-ticker.C:
			c.cleanup(ctx)
		}
	}
}

func (c *StaleNodeCleaner) cleanup(ctx context.Context) {
	nodes, err := c.nodes.GetAllOnline(ctx)
	if err != nil {
		c.logger.Error("failed to get online nodes", zap.Error(err))
		return
	}

	cutoff := time.Now().Add(-c.timeout)
	marked := 0
	affectedAccounts := map[string]struct{}{}
	for _, node := range nodes {
		if node.LastSeen != nil && node.LastSeen.Before(cutoff) {
			if err := c.nodes.UpdateStatus(ctx, node.ID, api.NodeStatusOffline); err != nil {
				c.logger.Error("failed to mark node offline",
					zap.String("node_id", node.ID),
					zap.Error(err),
				)
				continue
			}
			marked++
			affectedAccounts[node.AccountID] = struct{}{}
		}
	}

	if marked > 0 {
		c.logger.Info("marked stale nodes offline", zap.Int("count", marked))
		// Wake long-polling siblings on each affected account so they see
		// the offline transition immediately.
		for accountID := range affectedAccounts {
			c.events.Publish(accountID)
		}
	}
}
