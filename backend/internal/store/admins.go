package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrUsernameTaken = errors.New("username already exists")
var ErrAdminNotFound = errors.New("admin not found")

type Admin struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

// CreateAdmin inserts a new admin row. passwordHash must already be hashed (argon2id) -
// this layer never sees or stores a plaintext password.
func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash, role string) (Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx, `
		INSERT INTO admins (username, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, username, password_hash, role, created_at
	`, username, passwordHash, role).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return Admin{}, ErrUsernameTaken
		}
		return Admin{}, err
	}
	return a, nil
}

func (s *Store) GetAdminByUsername(ctx context.Context, username string) (Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, role, created_at
		FROM admins
		WHERE username = $1
	`, username).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrAdminNotFound
	}
	if err != nil {
		return Admin{}, err
	}
	return a, nil
}

func (s *Store) GetAdminByID(ctx context.Context, id string) (Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, role, created_at
		FROM admins
		WHERE id = $1
	`, id).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrAdminNotFound
	}
	if err != nil {
		return Admin{}, err
	}
	return a, nil
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&count)
	return count, err
}

// ListAdmins is used by the admin-facing Admin Users screen (PRD-admin-panel-ux.md
// §3.6). Deliberately doesn't return password_hash even internally - callers building
// a response never need it, so there's nothing to accidentally leak.
func (s *Store) ListAdmins(ctx context.Context) ([]Admin, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, username, role, created_at FROM admins ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admins []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(&a.ID, &a.Username, &a.Role, &a.CreatedAt); err != nil {
			return nil, err
		}
		admins = append(admins, a)
	}
	return admins, rows.Err()
}

// CountAdminsByRole backs the "never leave zero super admins" safeguard in
// handleUpdateAdminUser/handleDeleteAdminUser - checked immediately before a role
// change or delete would remove the last one, inside the same request rather than
// relying on the UI to prevent it.
func (s *Store) CountAdminsByRole(ctx context.Context, role string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admins WHERE role = $1`, role).Scan(&count)
	return count, err
}

// UpdateAdminRole changes an existing admin's role - the safeguard against demoting
// the last super_admin lives in the handler (it needs to count *before* this
// executes, and COUNT+UPDATE isn't meaningfully safer as one query here since role
// changes are rare, low-concurrency, super_admin-gated actions, not a hot path
// worth a transaction for).
func (s *Store) UpdateAdminRole(ctx context.Context, id, role string) (Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx, `
		UPDATE admins SET role = $2 WHERE id = $1
		RETURNING id, username, password_hash, role, created_at
	`, id, role).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrAdminNotFound
	}
	return a, err
}

// ResetAdminPassword overwrites an existing admin's password hash - used by a
// super_admin resetting another admin's forgotten password from the Admin Users
// screen, distinct from the self-service "change my own password" flow (which
// doesn't exist yet - out of scope here, same as before this change).
func (s *Store) ResetAdminPassword(ctx context.Context, id, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE admins SET password_hash = $2 WHERE id = $1`, id, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminNotFound
	}
	return nil
}

// DeleteAdmin removes an admin outright - the "don't delete yourself" and "don't
// delete the last super_admin" safeguards live in the handler, same reasoning as
// UpdateAdminRole above.
func (s *Store) DeleteAdmin(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM admins WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	// pgx surfaces Postgres error code 23505 (unique_violation) via *pgconn.PgError.
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == "23505"
	}
	return false
}
