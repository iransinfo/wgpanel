package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// deviceActiveWindow is PRD-account-management.md §6.4's rolling window: a source
// endpoint counts as a currently-active "device" iff its last observed handshake is
// within this window. Shared by the ingest-side limit check and the read-side
// online flag so the two can never disagree.
const deviceActiveWindow = 5 * time.Minute

// AccountDevice is one distinct client source endpoint ever observed for an account
// (see migration 0015). "Device" is an approximation by construction - WireGuard has
// no device identity, so NAT rebinding or roaming shows up as a new endpoint. That's
// exactly why the PRD makes enforcement soft by default.
type AccountDevice struct {
	ID             string
	AccountID      string
	SourceEndpoint string
	NodeID         string
	NodeName       string
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
}

// deviceObservation is one (account, endpoint) sighting extracted from a heartbeat's
// traffic report, already filtered to handshakes within deviceActiveWindow.
type deviceObservation struct {
	accountID string
	endpoint  string
	seenAt    time.Time
}

// ingestDeviceEndpoints batch-upserts this heartbeat's endpoint sightings and returns
// the distinct account ids touched, for the device-limit reconciliation pass.
func ingestDeviceEndpoints(ctx context.Context, tx pgx.Tx, nodeID string, observations []deviceObservation) ([]string, error) {
	if len(observations) == 0 {
		return nil, nil
	}

	// Postgres rejects an INSERT ... ON CONFLICT DO UPDATE whose values would touch the
	// same conflict-target row twice in one statement ("ON CONFLICT DO UPDATE command
	// cannot affect row a second time"), which would roll back the whole heartbeat
	// transaction. Two observations can share an (account_id, source_endpoint) - a
	// duplicated peer in the kernel snapshot, or several devices behind one NAT reusing
	// a source port - so collapse duplicates first, keeping the latest sighting.
	type endpointKey struct{ accountID, endpoint string }
	latest := make(map[endpointKey]time.Time, len(observations))
	for _, o := range observations {
		k := endpointKey{o.accountID, o.endpoint}
		if prev, ok := latest[k]; !ok || o.seenAt.After(prev) {
			latest[k] = o.seenAt
		}
	}

	accountIDs := make([]string, 0, len(latest))
	endpoints := make([]string, 0, len(latest))
	seenAts := make([]time.Time, 0, len(latest))
	touched := make(map[string]bool, len(latest))
	for k, seenAt := range latest {
		accountIDs = append(accountIDs, k.accountID)
		endpoints = append(endpoints, k.endpoint)
		seenAts = append(seenAts, seenAt)
		touched[k.accountID] = true
	}

	// GREATEST guards against a delayed/re-ordered heartbeat moving last_seen_at
	// backwards - the same idempotency stance the traffic counters take.
	if _, err := tx.Exec(ctx, `
		INSERT INTO account_devices (account_id, source_endpoint, node_id, first_seen_at, last_seen_at)
		SELECT v.account_id, v.endpoint, $4, v.seen_at, v.seen_at
		FROM unnest($1::uuid[], $2::text[], $3::timestamptz[]) AS v(account_id, endpoint, seen_at)
		ON CONFLICT (account_id, source_endpoint) DO UPDATE SET
			last_seen_at = GREATEST(account_devices.last_seen_at, EXCLUDED.last_seen_at),
			node_id = EXCLUDED.node_id
	`, accountIDs, endpoints, seenAts, nodeID); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	return ids, nil
}

// deviceLimitEvent is a state transition detected by reconcileDeviceLimitsTx, to be
// written to the audit log by the caller AFTER the surrounding transaction commits -
// an audit row describing a suspend that then rolled back would be worse than a
// best-effort insert.
type deviceLimitEvent struct {
	AccountID     string
	Action        string // account.device_limit_exceeded | account.device_limit_cleared
	ActiveDevices int
	DeviceLimit   int
	HardEnforced  bool // true when the exceeded event also auto-suspended the account
}

// reconcileDeviceLimitsTx applies PRD §6.4's soft enforcement for the given accounts:
// sets/clears device_limit_exceeded_at on window-count transitions, and - only for
// accounts with device_limit_hard_enforce - suspends on the way over the limit.
// Clearing never auto-unsuspends: lifting a hard-enforced suspension is a deliberate
// operator action (Enable), matching how manual/abuse suspensions already behave.
func reconcileDeviceLimitsTx(ctx context.Context, tx pgx.Tx, accountIDs []string) ([]deviceLimitEvent, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT a.id, a.device_limit, a.device_limit_hard_enforce, a.status,
		       a.device_limit_exceeded_at IS NOT NULL,
		       (SELECT count(*) FROM account_devices d
		        WHERE d.account_id = a.id AND d.last_seen_at > now() - make_interval(secs => $2)) AS active_devices
		FROM accounts a
		WHERE a.id = ANY($1::uuid[]) AND a.device_limit IS NOT NULL AND a.status != 'deleted'
		ORDER BY a.id
		FOR UPDATE OF a
	`, accountIDs, deviceActiveWindow.Seconds())
	if err != nil {
		return nil, err
	}
	type accountState struct {
		id            string
		limit         int
		hardEnforce   bool
		status        string
		flagged       bool
		activeDevices int
	}
	var states []accountState
	for rows.Next() {
		var st accountState
		if err := rows.Scan(&st.id, &st.limit, &st.hardEnforce, &st.status, &st.flagged, &st.activeDevices); err != nil {
			rows.Close()
			return nil, err
		}
		states = append(states, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var events []deviceLimitEvent
	for _, st := range states {
		exceeded := st.activeDevices > st.limit
		switch {
		case exceeded && !st.flagged:
			hardSuspend := st.hardEnforce && st.status == "active"
			if hardSuspend {
				if _, err := tx.Exec(ctx, `
					UPDATE accounts SET device_limit_exceeded_at = now(),
						status = 'suspended', suspend_reason = 'device_limit', updated_at = now()
					WHERE id = $1
				`, st.id); err != nil {
					return nil, err
				}
			} else {
				if _, err := tx.Exec(ctx,
					`UPDATE accounts SET device_limit_exceeded_at = now(), updated_at = now() WHERE id = $1`, st.id,
				); err != nil {
					return nil, err
				}
			}
			events = append(events, deviceLimitEvent{
				AccountID: st.id, Action: "account.device_limit_exceeded",
				ActiveDevices: st.activeDevices, DeviceLimit: st.limit, HardEnforced: hardSuspend,
			})
		case !exceeded && st.flagged:
			if _, err := tx.Exec(ctx,
				`UPDATE accounts SET device_limit_exceeded_at = NULL, updated_at = now() WHERE id = $1`, st.id,
			); err != nil {
				return nil, err
			}
			events = append(events, deviceLimitEvent{
				AccountID: st.id, Action: "account.device_limit_cleared",
				ActiveDevices: st.activeDevices, DeviceLimit: st.limit,
			})
		}
	}
	return events, nil
}

// ListAccountDevices returns every endpoint ever observed for this account (newest
// first), scoped to callerNamespace the same way every other account read is. The
// caller computes the "active right now" flag from LastSeenAt and DeviceActiveWindow.
func (s *Store) ListAccountDevices(ctx context.Context, accountID string, callerNamespace *string) ([]AccountDevice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.account_id, d.source_endpoint, d.node_id, n.name, d.first_seen_at, d.last_seen_at
		FROM account_devices d
		JOIN nodes n ON n.id = d.node_id
		JOIN accounts a ON a.id = d.account_id
		WHERE d.account_id = $2 AND `+namespaceFilter+`
		ORDER BY d.last_seen_at DESC
		LIMIT 200
	`, callerNamespace, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []AccountDevice
	for rows.Next() {
		var d AccountDevice
		if err := rows.Scan(&d.ID, &d.AccountID, &d.SourceEndpoint, &d.NodeID, &d.NodeName, &d.FirstSeenAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// DeviceActiveWindow exposes the PRD §6.4 window to httpapi so the read-side
// "active" flag uses the same cutoff the enforcement side does.
func DeviceActiveWindow() time.Duration { return deviceActiveWindow }

// PruneStaleDevices deletes endpoint rows unseen for the retention period - called
// from the same background sweep loop that flips stale nodes offline. Returns the
// number pruned, for the sweep's log line.
func (s *Store) PruneStaleDevices(ctx context.Context, unseenFor time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM account_devices WHERE last_seen_at < now() - make_interval(secs => $1)`,
		unseenFor.Seconds(),
	)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
