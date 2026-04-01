package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"valhalla/common/api"
)

type pgRouteRepo struct {
	pool *pgxpool.Pool
}

func NewRouteRepository(pool *pgxpool.Pool) RouteRepository {
	return &pgRouteRepo{pool: pool}
}

func (r *pgRouteRepo) Upsert(ctx context.Context, route *api.Route) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO routes (src_node_id, dst_node_id, path, cost, connection_type, relay_node_id, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT ON CONSTRAINT routes_pkey DO UPDATE
		 SET path=$3, cost=$4, connection_type=$5, relay_node_id=$6, created_at=$7, expires_at=$8`,
		route.SrcNodeID, route.DstNodeID, route.Path, route.Cost,
		route.ConnectionType, nullString(route.RelayNodeID),
		route.CreatedAt, route.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("upsert route: %w", err)
	}
	return nil
}

func (r *pgRouteRepo) GetOptimal(ctx context.Context, srcNodeID, dstNodeID string) (*api.Route, error) {
	var route api.Route
	var relayNodeID *string
	err := r.pool.QueryRow(ctx,
		`SELECT id, src_node_id, dst_node_id, path, cost, connection_type, relay_node_id, created_at, expires_at
		 FROM routes
		 WHERE src_node_id=$1 AND dst_node_id=$2 AND expires_at > NOW()
		 ORDER BY cost ASC LIMIT 1`,
		srcNodeID, dstNodeID,
	).Scan(&route.ID, &route.SrcNodeID, &route.DstNodeID, &route.Path, &route.Cost,
		&route.ConnectionType, &relayNodeID, &route.CreatedAt, &route.ExpiresAt)

	if err == pgx.ErrNoRows {
		return nil, api.ErrNoRouteAvailable
	}
	if err != nil {
		return nil, fmt.Errorf("get optimal route: %w", err)
	}
	if relayNodeID != nil {
		route.RelayNodeID = *relayNodeID
	}
	return &route, nil
}

func (r *pgRouteRepo) DeleteExpired(ctx context.Context) (int64, error) {
	result, err := r.pool.Exec(ctx,
		`DELETE FROM routes WHERE expires_at < $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired routes: %w", err)
	}
	return result.RowsAffected(), nil
}
