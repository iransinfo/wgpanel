package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// InsertAuditLog records a state-changing action. detail is optional structured data
// (a map, struct, or nil) marshaled to JSON here - callers never hand-construct JSON
// text themselves, which is what let a bare non-JSON string ("manual") reach this
// column and fail its ::jsonb cast in an earlier version of this function (caught via
// the Story 3 smoke test: a suspend call succeeded but its audit row silently never
// existed, since InsertAuditLog's error was logged, not surfaced). Making the
// signature take a Go value instead of a pre-formatted JSON string removes the
// possibility of that failure mode entirely rather than just fixing the one call site.
func (s *Store) InsertAuditLog(ctx context.Context, actor, action, target string, detail any, ipAddress string) error {
	var detailJSON []byte
	if detail != nil {
		var err error
		detailJSON, err = json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log (actor, action, target, detail, ip_address)
		VALUES ($1, $2, $3, $4, $5)
	`, actor, action, target, detailJSON, ipAddress)
	return err
}

type AuditLogEntry struct {
	ID        int64
	Actor     string
	Action    string
	Target    *string
	Detail    []byte // raw JSON, or nil - callers decode only if/when they need to
	IPAddress *string
	CreatedAt time.Time
}

// ListAuditLog powers the Audit Log screen (PRD-admin-panel-ux.md §3.7) - append-only
// like the table itself; there is deliberately no delete/filter-by-writing-back here,
// only a read path. limit is capped the same way ListAccounts caps its own limit.
func (s *Store) ListAuditLog(ctx context.Context, limit int) ([]AuditLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, actor, action, target, detail, ip_address, created_at
		FROM audit_log ORDER BY id DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IPAddress, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
