package db

import (
	"context"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"valhalla/common/api"
)

type pgNodeRepo struct {
	pool *pgxpool.Pool
}

func NewNodeRepository(pool *pgxpool.Pool) NodeRepository {
	return &pgNodeRepo{pool: pool}
}

func (r *pgNodeRepo) Create(ctx context.Context, node *api.NodeInfo) error {
	var internalIP *net.IP
	if node.InternalIP != "" {
		ip := net.ParseIP(node.InternalIP)
		internalIP = &ip
	}

	err := r.pool.QueryRow(ctx,
		`INSERT INTO nodes (account_id, name, node_type, os, public_key, endpoint, nat_type, internal_ip, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		node.AccountID, node.Name, node.NodeType, nullString(node.OS), node.PublicKey,
		nullString(node.Endpoint), nullString(string(node.NATType)),
		internalIP, node.Status,
	).Scan(&node.ID, &node.CreatedAt)
	if err != nil {
		return fmt.Errorf("create node: %w", err)
	}
	return nil
}

func (r *pgNodeRepo) GetByID(ctx context.Context, id string) (*api.NodeInfo, error) {
	var n api.NodeInfo
	var endpoint, natType, internalIP, osStr *string
	err := r.pool.QueryRow(ctx,
		`SELECT id, account_id, name, node_type, os, public_key, endpoint, nat_type,
		        host(internal_ip), status, last_seen, created_at
		 FROM nodes WHERE id=$1`,
		id,
	).Scan(&n.ID, &n.AccountID, &n.Name, &n.NodeType, &osStr, &n.PublicKey,
		&endpoint, &natType, &internalIP, &n.Status, &n.LastSeen, &n.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	if osStr != nil {
		n.OS = *osStr
	}
	if endpoint != nil {
		n.Endpoint = *endpoint
	}
	if natType != nil {
		n.NATType = api.NATType(*natType)
	}
	if internalIP != nil {
		n.InternalIP = *internalIP
	}
	return &n, nil
}

func (r *pgNodeRepo) GetByAccountID(ctx context.Context, accountID string) ([]*api.NodeInfo, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, name, node_type, os, public_key, endpoint, nat_type,
		        host(internal_ip), status, last_seen, created_at
		 FROM nodes WHERE account_id=$1 ORDER BY created_at`,
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("query nodes by account: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (r *pgNodeRepo) GetOnlineByType(ctx context.Context, nodeType api.NodeType) ([]*api.NodeInfo, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, name, node_type, os, public_key, endpoint, nat_type,
		        host(internal_ip), status, last_seen, created_at
		 FROM nodes WHERE node_type=$1 AND status='online'`,
		nodeType,
	)
	if err != nil {
		return nil, fmt.Errorf("query online nodes by type: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (r *pgNodeRepo) GetAllOnline(ctx context.Context) ([]*api.NodeInfo, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, name, node_type, os, public_key, endpoint, nat_type,
		        host(internal_ip), status, last_seen, created_at
		 FROM nodes WHERE status IN ('online', 'degraded')`,
	)
	if err != nil {
		return nil, fmt.Errorf("query all online nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (r *pgNodeRepo) UpdateEndpoint(ctx context.Context, nodeID, endpoint string, natType api.NATType) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE nodes SET endpoint=$2, nat_type=$3 WHERE id=$1`,
		nodeID, endpoint, string(natType),
	)
	return err
}

func (r *pgNodeRepo) UpdateStatus(ctx context.Context, nodeID string, status api.NodeStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE nodes SET status=$2 WHERE id=$1`,
		nodeID, string(status),
	)
	return err
}

func (r *pgNodeRepo) UpdateLastSeen(ctx context.Context, nodeID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE nodes SET last_seen=NOW(), status='online' WHERE id=$1`,
		nodeID,
	)
	return err
}

func (r *pgNodeRepo) Delete(ctx context.Context, nodeID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, nodeID)
	return err
}

func scanNodes(rows pgx.Rows) ([]*api.NodeInfo, error) {
	var nodes []*api.NodeInfo
	for rows.Next() {
		var n api.NodeInfo
		var endpoint, natType, internalIP, osStr *string
		if err := rows.Scan(&n.ID, &n.AccountID, &n.Name, &n.NodeType, &osStr, &n.PublicKey,
			&endpoint, &natType, &internalIP, &n.Status, &n.LastSeen, &n.CreatedAt); err != nil {
			return nil, err
		}
		if osStr != nil {
			n.OS = *osStr
		}
		if endpoint != nil {
			n.Endpoint = *endpoint
		}
		if natType != nil {
			n.NATType = api.NATType(*natType)
		}
		if internalIP != nil {
			n.InternalIP = *internalIP
		}
		nodes = append(nodes, &n)
	}
	return nodes, rows.Err()
}

func (r *pgNodeRepo) UpdateName(ctx context.Context, nodeID, name string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE nodes SET name=$2 WHERE id=$1`,
		nodeID, name,
	)
	return err
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
