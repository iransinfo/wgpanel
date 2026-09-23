package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestBackupRoundTrip proves DumpTables -> RestoreTables is lossless against a real
// database: the json_agg/json_populate_recordset pair is the entire backup format,
// so its fidelity (timestamps, jsonb detail, nullable columns, the audit_log
// sequence) is exactly what needs a live-Postgres test rather than a mock. Ends by
// restoring the state it dumped, so it leaves the database as it found it.
//
// Skipped unless WGPANEL_TEST_POSTGRES_DSN is set - see TestStoreIntegration.
func TestBackupRoundTrip(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run store integration tests")
	}

	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed rows exercising the trickier column types: jsonb (audit detail),
	// nullable text, timestamptz.
	if _, err := s.CreateAdmin(ctx, "backup-it-admin", "x-hash-x", "operator"); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := s.InsertAuditLog(ctx, "backup-it-admin", "test.seeded", "backup-test", map[string]any{"n": 1}, "127.0.0.1"); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	migrations, err := s.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("applied migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected applied migrations to be non-empty")
	}

	dump, err := s.DumpTables(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, table := range backupTables {
		if _, ok := dump[table]; !ok {
			t.Fatalf("dump missing table %s", table)
		}
	}

	adminsBefore, err := s.AdminCount(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate after the dump - restore must roll this back.
	if _, err := s.CreateAdmin(ctx, "backup-it-mutation", "x-hash-x", "support"); err != nil {
		t.Fatalf("mutate admin: %v", err)
	}

	counts, err := s.RestoreTables(ctx, dump)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if counts["admins"] != adminsBefore {
		t.Errorf("restored %d admins, want %d", counts["admins"], adminsBefore)
	}

	adminsAfter, err := s.AdminCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if adminsAfter != adminsBefore {
		t.Errorf("admin count after restore = %d, want %d (mutation should be gone)", adminsAfter, adminsBefore)
	}

	// The singleton settings row must survive (id=1 is seeded by migration and
	// assumed everywhere), and the audit sequence must have been resynced - a new
	// insert would otherwise collide with a restored id.
	if _, err := s.GetSettings(ctx); err != nil {
		t.Errorf("settings after restore: %v", err)
	}
	if err := s.InsertAuditLog(ctx, "backup-it-admin", "test.after_restore", "backup-test", nil, "127.0.0.1"); err != nil {
		t.Errorf("audit insert after restore (sequence resync): %v", err)
	}

	// Unknown table names must be rejected before anything is touched.
	if _, err := s.RestoreTables(ctx, map[string]json.RawMessage{"evil; DROP TABLE admins": json.RawMessage(`[]`)}); err == nil {
		t.Error("expected unknown-table restore to be rejected")
	}
}
