package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestStoreIntegration exercises the real SQL surface added in STORY-11 (migrations
// 0014-0017) against a live Postgres+TimescaleDB - migrations, bandwidth limits
// through the desired-peer feed, subscription token lookup/rotation, device
// tracking with soft and hard device-limit enforcement, and steering candidates.
//
// Skipped unless WGPANEL_TEST_POSTGRES_DSN points at a disposable database, e.g.:
//
//	docker run -d --rm -e POSTGRES_PASSWORD=test -e POSTGRES_DB=wgpanel -p 55433:5432 timescale/timescaledb:latest-pg16
//	WGPANEL_TEST_POSTGRES_DSN=postgres://postgres:test@localhost:55433/wgpanel go test ./internal/store
func TestStoreIntegration(t *testing.T) {
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

	nodeKey := "node-public-key"
	node, err := s.CreateNode(ctx, "it-node", "default", "eu", "vpn.test:51820", "10.99.0.0/24", 10, &nodeKey)
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if node.Region != "eu" {
		t.Fatalf("node region = %q, want eu", node.Region)
	}
	// Fresh nodes are 'pending'; account creation and steering need registered/online.
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}

	bw, deviceLimit := 25, 1
	acct, err := s.CreateAccount(ctx, CreateAccountParams{
		Label:               "it-account",
		PublicKey:           "it-account-public-key",
		PrivateKeyEncrypted: "encrypted",
		DeviceLimit:         &deviceLimit,
		BandwidthLimitMbps:  &bw,
		SubscriptionToken:   "it-token-original",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if acct.BandwidthLimitMbps == nil || *acct.BandwidthLimitMbps != 25 {
		t.Fatalf("bandwidth limit not persisted: %+v", acct.BandwidthLimitMbps)
	}
	if acct.SubscriptionToken != "it-token-original" {
		t.Fatalf("subscription token = %q", acct.SubscriptionToken)
	}

	t.Run("desired peers carry the bandwidth limit", func(t *testing.T) {
		peers, err := s.ListDesiredPeersForNode(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(peers) != 1 || peers[0].BandwidthLimitMbps == nil || *peers[0].BandwidthLimitMbps != 25 {
			t.Fatalf("desired peers = %+v", peers)
		}
	})

	t.Run("subscription token lookup and rotation", func(t *testing.T) {
		got, err := s.GetAccountBySubscriptionToken(ctx, "it-token-original")
		if err != nil || got.ID != acct.ID {
			t.Fatalf("lookup by token: %v %+v", err, got)
		}
		if _, err := s.RotateSubscriptionToken(ctx, acct.ID, nil, "it-token-rotated"); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if _, err := s.GetAccountBySubscriptionToken(ctx, "it-token-original"); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("old token should be dead, got %v", err)
		}
		if got, err = s.GetAccountBySubscriptionToken(ctx, "it-token-rotated"); err != nil || got.ID != acct.ID {
			t.Fatalf("new token lookup: %v", err)
		}
	})

	ingest := func(endpoint string) {
		t.Helper()
		now := time.Now()
		err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{
			PublicKey:     "it-account-public-key",
			ReceiveBytes:  1000,
			TransmitBytes: 1000,
			LastHandshake: &now,
			Endpoint:      endpoint,
		}}, nil)
		if err != nil {
			t.Fatalf("ingest heartbeat telemetry: %v", err)
		}
	}

	t.Run("device tracking and soft device-limit enforcement", func(t *testing.T) {
		ingest("203.0.113.7:1111")
		devices, err := s.ListAccountDevices(ctx, acct.ID, nil)
		if err != nil || len(devices) != 1 {
			t.Fatalf("devices after first sighting: %v %+v", err, devices)
		}
		a, err := s.GetAccount(ctx, acct.ID, nil)
		if err != nil || a.DeviceLimitExceededAt != nil {
			t.Fatalf("1 device with limit 1 must not be exceeded: %v %+v", err, a.DeviceLimitExceededAt)
		}

		ingest("203.0.113.8:2222") // second distinct endpoint inside the window
		a, err = s.GetAccount(ctx, acct.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.DeviceLimitExceededAt == nil {
			t.Fatal("2 devices with limit 1 should set the exceeded flag")
		}
		if a.Status != "active" {
			t.Fatalf("soft enforcement must not suspend, status = %q", a.Status)
		}
	})

	t.Run("flag clears once back under the limit", func(t *testing.T) {
		if _, err := s.pool.Exec(ctx, `DELETE FROM account_devices WHERE account_id = $1`, acct.ID); err != nil {
			t.Fatal(err)
		}
		ingest("203.0.113.9:3333") // one active device again
		a, err := s.GetAccount(ctx, acct.ID, nil)
		if err != nil || a.DeviceLimitExceededAt != nil {
			t.Fatalf("flag should clear when back under the limit: %v %+v", err, a.DeviceLimitExceededAt)
		}
	})

	t.Run("hard enforcement suspends", func(t *testing.T) {
		hard := true
		if _, err := s.UpdateAccount(ctx, acct.ID, nil, UpdateAccountParams{DeviceLimitHardEnforce: &hard}); err != nil {
			t.Fatal(err)
		}
		ingest("203.0.113.10:4444") // second device while hard enforcement is on
		a, err := s.GetAccount(ctx, acct.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != "suspended" || a.SuspendReason == nil || *a.SuspendReason != "device_limit" {
			t.Fatalf("expected device_limit suspension, got status=%q reason=%v", a.Status, a.SuspendReason)
		}

		// The suspension is real enforcement: the desired-peer feed drops the account.
		peers, err := s.ListDesiredPeersForNode(ctx, node.ID)
		if err != nil || len(peers) != 0 {
			t.Fatalf("suspended account must vanish from desired peers: %v %+v", err, peers)
		}

		// Lifting it is a deliberate operator action, and works.
		if _, err := s.EnableAccount(ctx, acct.ID, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("steering candidates", func(t *testing.T) {
		cands, err := s.SteerCandidatesForAccount(ctx, acct.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(cands) != 1 {
			t.Fatalf("expected 1 candidate, got %+v", cands)
		}
		c := cands[0]
		if c.NodeID != node.ID || !c.Online || c.Region != "eu" || c.Capacity != 10 || c.ActivePeers != 1 {
			t.Fatalf("candidate = %+v", c)
		}
	})

	t.Run("bandwidth limit cleared with the 0 sentinel", func(t *testing.T) {
		zero := 0
		a, err := s.UpdateAccount(ctx, acct.ID, nil, UpdateAccountParams{BandwidthLimitMbps: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if a.BandwidthLimitMbps != nil {
			t.Fatalf("0 should clear the limit, got %v", *a.BandwidthLimitMbps)
		}
	})

	t.Run("stale device prune", func(t *testing.T) {
		if _, err := s.pool.Exec(ctx,
			`UPDATE account_devices SET last_seen_at = now() - interval '31 days' WHERE account_id = $1`, acct.ID,
		); err != nil {
			t.Fatal(err)
		}
		pruned, err := s.PruneStaleDevices(ctx, 30*24*time.Hour)
		if err != nil || pruned == 0 {
			t.Fatalf("expected stale devices pruned, got %d, %v", pruned, err)
		}
	})
}
