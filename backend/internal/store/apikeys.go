package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrAPIKeyNotFound = errors.New("api key not found")
	ErrKeyIDTaken     = errors.New("key_id already exists")
)

type APIKey struct {
	ID                      string
	KeyID                   string
	SecretEncrypted         string
	PreviousSecretEncrypted *string
	PreviousSecretExpiresAt *time.Time
	Label                   string
	NodeGroups              []string
	Permissions             []string
	RevokedAt               *time.Time
	CreatedAt               time.Time
}

const apiKeyColumns = `id, key_id, secret_encrypted, previous_secret_encrypted, previous_secret_expires_at, label, node_groups, permissions, revoked_at, created_at`

func scanAPIKey(row pgx.Row, k *APIKey) error {
	return row.Scan(
		&k.ID, &k.KeyID, &k.SecretEncrypted, &k.PreviousSecretEncrypted, &k.PreviousSecretExpiresAt,
		&k.Label, &k.NodeGroups, &k.Permissions, &k.RevokedAt, &k.CreatedAt,
	)
}

func (s *Store) CreateAPIKey(ctx context.Context, keyID, secretEncrypted, label string, nodeGroups, permissions []string) (APIKey, error) {
	var k APIKey
	err := scanAPIKey(s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (key_id, secret_encrypted, label, node_groups, permissions)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+apiKeyColumns,
		keyID, secretEncrypted, label, nodeGroups, permissions,
	), &k)
	if err != nil {
		if isUniqueViolation(err) {
			return APIKey{}, ErrKeyIDTaken
		}
		return APIKey{}, err
	}
	return k, nil
}

func (s *Store) GetAPIKeyByKeyID(ctx context.Context, keyID string) (APIKey, error) {
	var k APIKey
	err := scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE key_id = $1`, keyID), &k)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrAPIKeyNotFound
	}
	return k, err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := scanAPIKey(rows, &k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// RotateAPIKeySecret moves the current secret to "previous" (valid until graceUntil,
// per PRD-telegram-bot-api.md §5.2's 24h rotation grace period) and installs
// newSecretEncrypted as current.
func (s *Store) RotateAPIKeySecret(ctx context.Context, id, newSecretEncrypted string, graceUntil time.Time) (APIKey, error) {
	var k APIKey
	err := scanAPIKey(s.pool.QueryRow(ctx, `
		UPDATE api_keys SET
			previous_secret_encrypted = secret_encrypted,
			previous_secret_expires_at = $2,
			secret_encrypted = $3
		WHERE id = $1
		RETURNING `+apiKeyColumns,
		id, graceUntil, newSecretEncrypted,
	), &k)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrAPIKeyNotFound
	}
	return k, err
}
