package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// backupTables is every table included in a panel backup, in an order that
// satisfies FK dependencies on insert (accounts/nodes before account_peers, which
// references both). The two TimescaleDB hypertables (peer_traffic_samples,
// node_metrics) are deliberately excluded: they're high-volume rolling history
// whose durable aggregate already lives on accounts.data_used_bytes, so a backup
// stays small and restorable while losing only chart history.
var backupTables = []string{
	"admins",
	"nodes",
	"accounts",
	"account_peers",
	"account_devices",
	"api_keys",
	"panel_settings",
	"audit_log",
}

// AppliedMigrations returns the filenames recorded in schema_migrations, sorted.
// A backup embeds this list so a restore can refuse a file taken on a different
// schema version (json_populate_recordset would silently NULL any column the old
// schema didn't have, then fail on the first NOT NULL - or worse, not fail).
func (s *Store) AppliedMigrations(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT filename FROM schema_migrations ORDER BY filename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DumpTables serializes every backup table to JSON rows inside one repeatable-read
// read-only transaction, so the dump is a consistent snapshot even while agents
// are heartbeating writes into nodes/accounts. Table names are interpolated into
// SQL but only ever come from the compile-time backupTables list.
func (s *Store) DumpTables(ctx context.Context) (map[string]json.RawMessage, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	out := make(map[string]json.RawMessage, len(backupTables))
	for _, table := range backupTables {
		var rows string
		err := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(t), '[]'::json)::text FROM `+table+` t`).Scan(&rows)
		if err != nil {
			return nil, fmt.Errorf("dump %s: %w", table, err)
		}
		out[table] = json.RawMessage(rows)
	}
	return out, tx.Commit(ctx)
}

// RestoreTables replaces the contents of every backup table with the given rows,
// atomically: one transaction truncates them all (single statement, so FK order
// doesn't matter for the delete half) and re-inserts in dependency order via
// json_populate_recordset - the exact inverse of DumpTables' json_agg, which is
// what makes this schema-generic instead of a hand-written struct per table.
// Returns per-table restored row counts. A table absent from the map is left
// empty, not skipped - the restored panel must reflect the backup, not a merge.
func (s *Store) RestoreTables(ctx context.Context, tables map[string]json.RawMessage) (map[string]int, error) {
	known := make(map[string]bool, len(backupTables))
	for _, t := range backupTables {
		known[t] = true
	}
	for name := range tables {
		if !known[name] {
			return nil, fmt.Errorf("backup contains unknown table %q", name)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(backupTables, ", ")+` CASCADE`); err != nil {
		return nil, fmt.Errorf("truncate: %w", err)
	}

	counts := make(map[string]int, len(backupTables))
	for _, table := range backupTables {
		rows, ok := tables[table]
		if !ok {
			counts[table] = 0
			continue
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO `+table+` SELECT * FROM json_populate_recordset(NULL::`+table+`, $1::json)`,
			string(rows),
		)
		if err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
		counts[table] = int(tag.RowsAffected())
	}

	// audit_log.id is BIGSERIAL - its sequence lives outside the rows, so after
	// inserting explicit ids the next nextval() would collide with a restored row.
	if _, err := tx.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('audit_log', 'id'), COALESCE((SELECT MAX(id) FROM audit_log), 0) + 1, false)
	`); err != nil {
		return nil, fmt.Errorf("resync audit_log sequence: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return counts, nil
}
