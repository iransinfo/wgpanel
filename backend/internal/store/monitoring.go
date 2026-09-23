package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// PeerTrafficReport is one peer's raw cumulative WireGuard counters as reported by
// an agent in a single heartbeat (docs/PRD-monitoring-stats.md, docs/STORY-10-
// monitoring-and-domain-management.md). Cumulative, not deltas - the agent doesn't
// keep any local state; the server holds the authoritative "last seen" value and
// computes deltas itself, which is what makes a redelivered/retried heartbeat
// naturally idempotent (reported == last_seen => delta 0) with no separate dedup key.
type PeerTrafficReport struct {
	PublicKey     string
	ReceiveBytes  int64
	TransmitBytes int64
	LastHandshake *time.Time // nil if this peer has never completed a handshake
	// Endpoint is the peer's current client source "ip:port" as the kernel reports
	// it, or "" if unknown. Only trusted as a live device sighting when LastHandshake
	// is inside deviceActiveWindow - the kernel remembers a peer's last endpoint
	// indefinitely, long after that client disconnected (PRD §6.4 device tracking).
	Endpoint string
}

// NodeMetricsReport is one node's best-effort CPU/RAM sample. All fields nil means
// the agent couldn't read them this cycle (e.g. non-Linux) - a nil report should be
// dropped by the caller rather than written as a row of nulls.
type NodeMetricsReport struct {
	CPUPercent    *float32
	MemUsedBytes  *int64
	MemTotalBytes *int64
}

// IngestHeartbeatTelemetry turns one node's heartbeat traffic/metrics report into
// real accounting: per-peer traffic samples, accounts.data_used_bytes, and a node
// health sample. Everything for this node happens in one transaction with every
// account_peers row for it locked up front (FOR UPDATE) - the same transactional-
// locking idiom CreateAccount already uses for IP allocation - so two concurrent or
// retried heartbeats for the same node can never each read the same stale "last
// seen" counter and double-count the same traffic.
//
// A peer's first-ever observation only SEEDS last_receive_bytes/last_transmit_bytes
// (both NULL beforehand - see migration 0011) - it inserts no sample and touches no
// account's data_used_bytes. Only the second observation onward computes a real
// delta. Skipping this distinction would mean the very first heartbeat after this
// shipped (or after a brand-new account_peers row is created) would attribute a
// peer's entire lifetime cumulative WireGuard counter to a single ~10s tick - caught
// in design review before implementation, not discovered after shipping.
//
// All per-peer work for this heartbeat is batched into one UPDATE, one INSERT (via
// COPY), and one accounts UPDATE - never one query per peer - per this project's own
// docs/PRD-monitoring-stats.md §7 requirement ("batched writes, not one row per peer
// per HTTP call").
func (s *Store) IngestHeartbeatTelemetry(ctx context.Context, nodeID string, traffic []PeerTrafficReport, metrics *NodeMetricsReport) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var deviceEvents []deviceLimitEvent
	if len(traffic) > 0 {
		accountByKey, err := ingestPeerTraffic(ctx, tx, nodeID, traffic)
		if err != nil {
			return err
		}

		// Device tracking (PRD-account-management.md §6.4): a peer's reported endpoint
		// only counts as a live device sighting when its handshake is inside the
		// rolling window - see PeerTrafficReport.Endpoint's doc comment.
		var observations []deviceObservation
		for _, t := range traffic {
			accountID, known := accountByKey[t.PublicKey]
			if !known || t.Endpoint == "" || t.LastHandshake == nil {
				continue
			}
			if time.Since(*t.LastHandshake) > deviceActiveWindow {
				continue
			}
			observations = append(observations, deviceObservation{
				accountID: accountID, endpoint: t.Endpoint, seenAt: *t.LastHandshake,
			})
		}
		touchedAccounts, err := ingestDeviceEndpoints(ctx, tx, nodeID, observations)
		if err != nil {
			return err
		}
		deviceEvents, err = reconcileDeviceLimitsTx(ctx, tx, touchedAccounts)
		if err != nil {
			return err
		}
	}
	if metrics != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_metrics (ts, node_id, cpu_percent, mem_used_bytes, mem_total_bytes)
			VALUES (now(), $1, $2, $3, $4)
		`, nodeID, metrics.CPUPercent, metrics.MemUsedBytes, metrics.MemTotalBytes); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Audit rows only after the transaction they describe actually committed -
	// best-effort by design, same as every handler's InsertAuditLog call.
	for _, ev := range deviceEvents {
		detail := map[string]any{
			"active_devices": ev.ActiveDevices,
			"device_limit":   ev.DeviceLimit,
			"hard_enforced":  ev.HardEnforced,
		}
		_ = s.InsertAuditLog(ctx, "system", ev.Action, ev.AccountID, detail, "")
	}

	return nil
}

// ingestPeerTraffic returns the node's publicKey -> accountID mapping it already had
// to build for counter attribution, so the device-tracking pass that follows doesn't
// re-query it.
func ingestPeerTraffic(ctx context.Context, tx pgx.Tx, nodeID string, traffic []PeerTrafficReport) (map[string]string, error) {
	type existingPeer struct {
		id                string
		accountID         string
		lastReceiveBytes  *int64
		lastTransmitBytes *int64
	}

	rows, err := tx.Query(ctx, `
		SELECT ap.id, ap.account_id, a.public_key, ap.last_receive_bytes, ap.last_transmit_bytes
		FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.node_id = $1
		FOR UPDATE OF ap
	`, nodeID)
	if err != nil {
		return nil, err
	}
	byPublicKey := make(map[string]existingPeer)
	for rows.Next() {
		var p existingPeer
		var publicKey string
		if err := rows.Scan(&p.id, &p.accountID, &publicKey, &p.lastReceiveBytes, &p.lastTransmitBytes); err != nil {
			rows.Close()
			return nil, err
		}
		byPublicKey[publicKey] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var updateIDs []string
	var updateRx, updateTx []int64
	var updateHandshake []*time.Time
	var sampleAccountIDs []string
	var sampleRx, sampleTx []int64
	deltaByAccount := make(map[string]int64)

	for _, t := range traffic {
		peer, ok := byPublicKey[t.PublicKey]
		if !ok {
			continue // reported peer isn't (or no longer is) known to this node - ignore
		}

		updateIDs = append(updateIDs, peer.id)
		updateRx = append(updateRx, t.ReceiveBytes)
		updateTx = append(updateTx, t.TransmitBytes)
		updateHandshake = append(updateHandshake, t.LastHandshake)

		if peer.lastReceiveBytes == nil || peer.lastTransmitBytes == nil {
			continue // first observation - seed only, see doc comment above
		}

		rxDelta := t.ReceiveBytes - *peer.lastReceiveBytes
		if rxDelta < 0 {
			rxDelta = t.ReceiveBytes // counter reset (interface restart) - count the new value as the full delta, not a negative one
		}
		txDelta := t.TransmitBytes - *peer.lastTransmitBytes
		if txDelta < 0 {
			txDelta = t.TransmitBytes
		}
		if rxDelta == 0 && txDelta == 0 {
			continue // idle peer this tick - skip the sample so the hypertable doesn't fill with all-zero rows
		}

		sampleAccountIDs = append(sampleAccountIDs, peer.accountID)
		sampleRx = append(sampleRx, rxDelta)
		sampleTx = append(sampleTx, txDelta)
		deltaByAccount[peer.accountID] += rxDelta + txDelta
	}

	if len(updateIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE account_peers AS ap SET
				last_receive_bytes = v.rx,
				last_transmit_bytes = v.tx,
				last_handshake_at = COALESCE(v.hs, ap.last_handshake_at)
			FROM (
				SELECT * FROM unnest($1::uuid[], $2::bigint[], $3::bigint[], $4::timestamptz[])
					AS v(id, rx, tx, hs)
			) AS v
			WHERE ap.id = v.id
		`, updateIDs, updateRx, updateTx, updateHandshake); err != nil {
			return nil, err
		}
	}

	if len(sampleAccountIDs) > 0 {
		nodeIDs := make([]string, len(sampleAccountIDs))
		for i := range nodeIDs {
			nodeIDs[i] = nodeID
		}
		now := time.Now()
		rowsToCopy := make([][]any, len(sampleAccountIDs))
		for i := range sampleAccountIDs {
			rowsToCopy[i] = []any{now, sampleAccountIDs[i], nodeIDs[i], sampleRx[i], sampleTx[i]}
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"peer_traffic_samples"},
			[]string{"ts", "account_id", "node_id", "rx_delta", "tx_delta"},
			pgx.CopyFromRows(rowsToCopy),
		); err != nil {
			return nil, err
		}
	}

	if len(deltaByAccount) > 0 {
		accountIDs := make([]string, 0, len(deltaByAccount))
		deltas := make([]int64, 0, len(deltaByAccount))
		for id, delta := range deltaByAccount {
			accountIDs = append(accountIDs, id)
			deltas = append(deltas, delta)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE accounts AS a SET data_used_bytes = a.data_used_bytes + v.delta
			FROM (SELECT * FROM unnest($1::uuid[], $2::bigint[]) AS v(account_id, delta)) AS v
			WHERE a.id = v.account_id
		`, accountIDs, deltas); err != nil {
			return nil, err
		}
	}

	accountByKey := make(map[string]string, len(byPublicKey))
	for publicKey, peer := range byPublicKey {
		accountByKey[publicKey] = peer.accountID
	}
	return accountByKey, nil
}

// UsageSample is one time-bucketed point in an account's usage-over-time series.
type UsageSample struct {
	Bucket  time.Time
	RxBytes int64
	TxBytes int64
}

// AccountUsageSeries sums this account's traffic samples (across every node it has a
// peer on) into bucket-sized points, for the account detail usage chart. bucket must
// be "hour" or "day" - validated by the caller (httpapi), not here.
func (s *Store) AccountUsageSeries(ctx context.Context, accountID, bucket string, from, to time.Time) ([]UsageSample, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT time_bucket($1::interval, ts) AS bucket, sum(rx_delta), sum(tx_delta)
		FROM peer_traffic_samples
		WHERE account_id = $2 AND ts >= $3 AND ts <= $4
		GROUP BY bucket
		ORDER BY bucket
	`, bucketInterval(bucket), accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []UsageSample
	for rows.Next() {
		var s UsageSample
		if err := rows.Scan(&s.Bucket, &s.RxBytes, &s.TxBytes); err != nil {
			return nil, err
		}
		samples = append(samples, s)
	}
	return samples, rows.Err()
}

// MetricsSample is one time-bucketed point in a node's CPU/RAM history.
type MetricsSample struct {
	Bucket        time.Time
	CPUPercent    *float32
	MemUsedBytes  *int64
	MemTotalBytes *int64
}

// NodeMetricsSeries averages this node's raw metric samples into bucket-sized
// points, for the node health chart.
func (s *Store) NodeMetricsSeries(ctx context.Context, nodeID string, from, to time.Time) ([]MetricsSample, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT time_bucket('1 hour'::interval, ts) AS bucket,
		       avg(cpu_percent), avg(mem_used_bytes)::bigint, avg(mem_total_bytes)::bigint
		FROM node_metrics
		WHERE node_id = $1 AND ts >= $2 AND ts <= $3
		GROUP BY bucket
		ORDER BY bucket
	`, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []MetricsSample
	for rows.Next() {
		var m MetricsSample
		var cpu *float64
		if err := rows.Scan(&m.Bucket, &cpu, &m.MemUsedBytes, &m.MemTotalBytes); err != nil {
			return nil, err
		}
		if cpu != nil {
			f := float32(*cpu)
			m.CPUPercent = &f
		}
		samples = append(samples, m)
	}
	return samples, rows.Err()
}

func bucketInterval(bucket string) string {
	if bucket == "day" {
		return "1 day"
	}
	return "1 hour"
}
