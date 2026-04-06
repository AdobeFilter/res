package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"valhalla/common/api"
)

type pgAccountRepo struct {
	pool *pgxpool.Pool
}

func NewAccountRepository(pool *pgxpool.Pool) AccountRepository {
	return &pgAccountRepo{pool: pool}
}

func (r *pgAccountRepo) Create(ctx context.Context, email, passwordHash string) (*api.Account, error) {
	var acc api.Account
	err := r.pool.QueryRow(ctx,
		`INSERT INTO accounts (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, created_at, updated_at`,
		email, passwordHash,
	).Scan(&acc.ID, &acc.Email, &acc.CreatedAt, &acc.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}

	// Create default settings
	_, err = r.pool.Exec(ctx,
		`INSERT INTO account_settings (account_id) VALUES ($1)`, acc.ID)
	if err != nil {
		return nil, fmt.Errorf("create default settings: %w", err)
	}

	return &acc, nil
}

func (r *pgAccountRepo) GetByEmail(ctx context.Context, email string) (*api.Account, error) {
	var acc api.Account
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at, updated_at FROM accounts WHERE email=$1`,
		email,
	).Scan(&acc.ID, &acc.Email, &acc.PasswordHash, &acc.CreatedAt, &acc.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account by email: %w", err)
	}
	return &acc, nil
}

func (r *pgAccountRepo) GetByID(ctx context.Context, id string) (*api.Account, error) {
	var acc api.Account
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at, updated_at FROM accounts WHERE id=$1`,
		id,
	).Scan(&acc.ID, &acc.Email, &acc.PasswordHash, &acc.CreatedAt, &acc.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account by id: %w", err)
	}
	return &acc, nil
}

// --- Account Settings ---

type pgAccountSettingsRepo struct {
	pool *pgxpool.Pool
}

func NewAccountSettingsRepository(pool *pgxpool.Pool) AccountSettingsRepository {
	return &pgAccountSettingsRepo{pool: pool}
}

func (r *pgAccountSettingsRepo) Get(ctx context.Context, accountID string) (*api.AccountSettings, error) {
	var s api.AccountSettings
	var exitNodeID *string
	err := r.pool.QueryRow(ctx,
		`SELECT account_id, vless_enabled, exit_node_id, updated_at FROM account_settings WHERE account_id=$1`,
		accountID,
	).Scan(&s.AccountID, &s.VLESSEnabled, &exitNodeID, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account settings: %w", err)
	}
	if exitNodeID != nil {
		s.ExitNodeID = *exitNodeID
	}
	return &s, nil
}

func (r *pgAccountSettingsRepo) Upsert(ctx context.Context, accountID string, vlessEnabled bool) (*api.AccountSettings, error) {
	var s api.AccountSettings
	var exitNodeID *string
	err := r.pool.QueryRow(ctx,
		`INSERT INTO account_settings (account_id, vless_enabled, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (account_id) DO UPDATE SET vless_enabled=$2, updated_at=NOW()
		 RETURNING account_id, vless_enabled, exit_node_id, updated_at`,
		accountID, vlessEnabled,
	).Scan(&s.AccountID, &s.VLESSEnabled, &exitNodeID, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert account settings: %w", err)
	}
	if exitNodeID != nil {
		s.ExitNodeID = *exitNodeID
	}
	return &s, nil
}

func (r *pgAccountSettingsRepo) SetExitNode(ctx context.Context, accountID string, exitNodeID *string) (*api.AccountSettings, error) {
	var s api.AccountSettings
	var outExitNodeID *string
	err := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET exit_node_id=$2, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING account_id, vless_enabled, exit_node_id, updated_at`,
		accountID, exitNodeID,
	).Scan(&s.AccountID, &s.VLESSEnabled, &outExitNodeID, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("set exit node: %w", err)
	}
	if outExitNodeID != nil {
		s.ExitNodeID = *outExitNodeID
	}
	return &s, nil
}
