-- Distinct source endpoints (client IP:port) observed per account, reported by node
-- agents from the kernel's per-peer endpoint state on every heartbeat - the basis for
-- device-limit soft enforcement (docs/PRD-account-management.md §6.4). One row per
-- (account, endpoint) ever seen; "currently active" is computed at read time as
-- last_seen_at within the rolling 5-minute window the PRD specifies, never stored as
-- a boolean that could go stale.
CREATE TABLE account_devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source_endpoint TEXT NOT NULL,  -- "ip:port" exactly as WireGuard reports it
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, -- last node this endpoint was seen on
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, source_endpoint)
);

CREATE INDEX idx_account_devices_account_last_seen ON account_devices (account_id, last_seen_at DESC);
-- For the retention prune (rows unseen for 30 days are deleted by the sweep loop).
CREATE INDEX idx_account_devices_last_seen ON account_devices (last_seen_at);

-- Soft enforcement is a detection signal, not a cutoff (PRD §6.4): when distinct
-- active endpoints exceed device_limit, device_limit_exceeded_at is set (and cleared
-- once back under). device_limit_hard_enforce is the PRD's per-account opt-in toggle
-- that upgrades the signal to an automatic suspend (suspend_reason 'device_limit').
ALTER TABLE accounts ADD COLUMN device_limit_hard_enforce BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE accounts ADD COLUMN device_limit_exceeded_at TIMESTAMPTZ;
