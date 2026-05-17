package db

import (
	"context"
	"encoding/json"
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

// accountColumns is the SELECT list used by every read path; keeping it in
// one place means adding a column = touch this const + the Scan() calls.
const accountColumns = `id, email, password_hash, created_at, updated_at, tier, remnawave_uuid, subscription_url`

func scanAccount(row pgx.Row, acc *api.Account) error {
	var rwUUID, subURL *string
	if err := row.Scan(
		&acc.ID, &acc.Email, &acc.PasswordHash, &acc.CreatedAt, &acc.UpdatedAt,
		&acc.Tier, &rwUUID, &subURL,
	); err != nil {
		return err
	}
	if rwUUID != nil {
		acc.RemnawaveUUID = *rwUUID
	}
	if subURL != nil {
		acc.SubscriptionURL = *subURL
	}
	return nil
}

func (r *pgAccountRepo) Create(ctx context.Context, email, passwordHash string) (*api.Account, error) {
	var acc api.Account
	err := r.pool.QueryRow(ctx,
		`INSERT INTO accounts (email, password_hash) VALUES ($1, $2)
		 RETURNING `+accountColumns,
		email, passwordHash,
	).Scan(&acc.ID, &acc.Email, &acc.PasswordHash, &acc.CreatedAt, &acc.UpdatedAt,
		&acc.Tier, new(*string), new(*string))
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
	row := r.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE email=$1`,
		email,
	)
	if err := scanAccount(row, &acc); err != nil {
		if err == pgx.ErrNoRows {
			return nil, api.ErrNotFound
		}
		return nil, fmt.Errorf("get account by email: %w", err)
	}
	return &acc, nil
}

func (r *pgAccountRepo) GetByID(ctx context.Context, id string) (*api.Account, error) {
	var acc api.Account
	row := r.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id=$1`, id,
	)
	if err := scanAccount(row, &acc); err != nil {
		if err == pgx.ErrNoRows {
			return nil, api.ErrNotFound
		}
		return nil, fmt.Errorf("get account by id: %w", err)
	}
	return &acc, nil
}

func (r *pgAccountRepo) Delete(ctx context.Context, id string) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM accounts WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return api.ErrNotFound
	}
	return nil
}

func (r *pgAccountRepo) SetRemnawaveLink(ctx context.Context, accountID, remnawaveUUID, subscriptionURL string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE accounts SET remnawave_uuid=$2, subscription_url=$3, updated_at=NOW() WHERE id=$1`,
		accountID, remnawaveUUID, subscriptionURL,
	)
	if err != nil {
		return fmt.Errorf("set remnawave link: %w", err)
	}
	return nil
}

// --- Account Settings ---

type pgAccountSettingsRepo struct {
	pool *pgxpool.Pool
}

func NewAccountSettingsRepository(pool *pgxpool.Pool) AccountSettingsRepository {
	return &pgAccountSettingsRepo{pool: pool}
}

// All-columns SELECT list used by every read path here. Keeps the scan order
// in one place so adding a column means touching this const + scanSettings.
const settingsSelect = `account_id, vless_enabled, exit_node_id, exit_nodes,
	routing_rules, fragment_enabled, block_ads_enabled, updated_at`

// scanSettings decodes a row produced by settingsSelect into AccountSettings.
// Handles the nullable exit_node_id and the JSONB exit_nodes column.
func scanSettings(row pgx.Row) (*api.AccountSettings, error) {
	var s api.AccountSettings
	var exitNodeID *string
	var exitNodesRaw []byte
	err := row.Scan(
		&s.AccountID,
		&s.VLESSEnabled,
		&exitNodeID,
		&exitNodesRaw,
		&s.RoutingRules,
		&s.FragmentEnabled,
		&s.BlockAdsEnabled,
		&s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if exitNodeID != nil {
		s.ExitNodeID = *exitNodeID
	}
	if len(exitNodesRaw) > 0 {
		if err := json.Unmarshal(exitNodesRaw, &s.ExitNodes); err != nil {
			return nil, fmt.Errorf("decode exit_nodes: %w", err)
		}
	}
	if s.ExitNodes == nil {
		s.ExitNodes = []api.ExitNodeConfig{}
	}
	return &s, nil
}

func (r *pgAccountSettingsRepo) Get(ctx context.Context, accountID string) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+settingsSelect+` FROM account_settings WHERE account_id=$1`,
		accountID,
	)
	s, err := scanSettings(row)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account settings: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) Upsert(ctx context.Context, accountID string, vlessEnabled bool) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`INSERT INTO account_settings (account_id, vless_enabled, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (account_id) DO UPDATE SET vless_enabled=$2, updated_at=NOW()
		 RETURNING `+settingsSelect,
		accountID, vlessEnabled,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("upsert account settings: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) SetExitNode(ctx context.Context, accountID string, exitNodeID *string) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET exit_node_id=$2, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING `+settingsSelect,
		accountID, exitNodeID,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("set exit node: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) SetExitNodes(ctx context.Context, accountID string, nodes []api.ExitNodeConfig) (*api.AccountSettings, error) {
	if nodes == nil {
		nodes = []api.ExitNodeConfig{}
	}
	payload, err := json.Marshal(nodes)
	if err != nil {
		return nil, fmt.Errorf("encode exit_nodes: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET exit_nodes=$2::jsonb, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING `+settingsSelect,
		accountID, payload,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("set exit nodes: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) SetRoutingRules(ctx context.Context, accountID string, rules string) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET routing_rules=$2, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING `+settingsSelect,
		accountID, rules,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("set routing rules: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) SetFragmentEnabled(ctx context.Context, accountID string, enabled bool) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET fragment_enabled=$2, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING `+settingsSelect,
		accountID, enabled,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("set fragment enabled: %w", err)
	}
	return s, nil
}

func (r *pgAccountSettingsRepo) SetBlockAdsEnabled(ctx context.Context, accountID string, enabled bool) (*api.AccountSettings, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE account_settings SET block_ads_enabled=$2, updated_at=NOW()
		 WHERE account_id=$1
		 RETURNING `+settingsSelect,
		accountID, enabled,
	)
	s, err := scanSettings(row)
	if err != nil {
		return nil, fmt.Errorf("set block ads enabled: %w", err)
	}
	return s, nil
}
