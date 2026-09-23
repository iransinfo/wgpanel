-- Real per-account traffic accounting and per-node health metrics
-- (docs/PRD-monitoring-stats.md, scoped down for this pass - see docs/STORY-10).
--
-- last_receive_bytes/last_transmit_bytes are deliberately NULLable with NO default.
-- NULL means "never observed yet" - the first heartbeat for a peer only SEEDS these
-- columns (no sample inserted, no data_used_bytes change); only the SECOND heartbeat
-- onward computes a real delta. Without this NULL sentinel, defaulting to 0 would mean
-- the very first heartbeat after this migration ships attributes a peer's entire
-- lifetime cumulative WireGuard counter (which may represent days of real traffic) to
-- a single ~10-second tick - a real bug caught in design review before implementation.
ALTER TABLE account_peers ADD COLUMN last_receive_bytes BIGINT;
ALTER TABLE account_peers ADD COLUMN last_transmit_bytes BIGINT;

-- Last-reported WireGuard handshake time for this peer, overwritten every heartbeat
-- (no NULL-sentinel complexity needed here, unlike the counters above - a timestamp
-- has no "reset" ambiguity, it's just whatever the agent most recently reported, or
-- NULL if this peer has never handshaked at all). Online/offline (PRD-monitoring-
-- stats.md §5: online iff within the last 180s) is computed at read time from this,
-- not stored as a separate boolean that could go stale.
ALTER TABLE account_peers ADD COLUMN last_handshake_at TIMESTAMPTZ;

-- One row per (account, node, heartbeat-tick-with-traffic) - the delta since the last
-- observation, not a cumulative counter. Retained 7 days raw (see retention policy
-- below); continuous aggregates/longer rollups are deliberately deferred (see
-- docs/STORY-10) - revisit before onboarding real traffic volume, since retrofitting
-- rollups onto a hypertable that already has billions of rows is a much heavier
-- operation than building them now while it's empty.
CREATE TABLE peer_traffic_samples (
    ts         TIMESTAMPTZ NOT NULL,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    node_id    UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    rx_delta   BIGINT NOT NULL,
    tx_delta   BIGINT NOT NULL
);
SELECT create_hypertable('peer_traffic_samples', 'ts', if_not_exists => true);
CREATE INDEX idx_peer_traffic_samples_account_ts ON peer_traffic_samples (account_id, ts DESC);
SELECT add_retention_policy('peer_traffic_samples', INTERVAL '7 days');

-- One row per node per metrics-reporting tick (a lower cadence than the 10s
-- peer-reconciliation heartbeat - see cmd/agent/heartbeat.go). Retained 30 days.
CREATE TABLE node_metrics (
    ts             TIMESTAMPTZ NOT NULL,
    node_id        UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    cpu_percent    REAL,
    mem_used_bytes BIGINT,
    mem_total_bytes BIGINT
);
SELECT create_hypertable('node_metrics', 'ts', if_not_exists => true);
CREATE INDEX idx_node_metrics_node_ts ON node_metrics (node_id, ts DESC);
SELECT add_retention_policy('node_metrics', INTERVAL '30 days');
