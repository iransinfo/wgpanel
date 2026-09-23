package store

import (
	"context"

	"wgpanel-api/internal/steering"
)

// steeringCPUWindow is how far back the per-node CPU average looks. Agents report
// CPU every ~40s (metricsEveryNTicks x the 10s heartbeat), so 15 minutes is ~20
// samples - enough to smooth spikes without steering on stale data.
const steeringCPUWindow = "15 minutes"

// SteerCandidatesForAccount gathers, for every node this account has a peer on and
// that could actually serve a connection (a WireGuard public key exists), the raw
// inputs steering.Rank scores: current active-peer load, capacity, and recent average
// CPU. Namespace-scoped like every other account read so an API key can't probe node
// load through an account it doesn't own.
func (s *Store) SteerCandidatesForAccount(ctx context.Context, accountID string, callerNamespace *string) ([]steering.Candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.name, n.region, n.public_endpoint, n.status = 'online',
		       n.capacity_max_peers,
		       (SELECT count(*) FROM account_peers ap2
		        JOIN accounts a2 ON a2.id = ap2.account_id
		        WHERE ap2.node_id = n.id AND a2.status = 'active') AS active_peers,
		       (SELECT avg(cpu_percent)::float8 FROM node_metrics m
		        WHERE m.node_id = n.id AND m.ts > now() - $3::interval) AS recent_cpu
		FROM account_peers ap
		JOIN nodes n ON n.id = ap.node_id
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.account_id = $2 AND `+namespaceFilter+`
		  AND n.public_key IS NOT NULL AND n.public_key != ''
	`, callerNamespace, accountID, steeringCPUWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []steering.Candidate
	for rows.Next() {
		var c steering.Candidate
		if err := rows.Scan(&c.NodeID, &c.Name, &c.Region, &c.PublicEndpoint, &c.Online, &c.Capacity, &c.ActivePeers, &c.CPUPercent); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}
