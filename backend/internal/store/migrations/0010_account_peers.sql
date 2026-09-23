-- An account (subscription identity: keypair, quota, expiry, device limit, status) is
-- no longer tied to exactly one node - the same account gets a WireGuard peer on
-- every currently eligible node, not just whichever one it happened to be created
-- against. account_peers is the join table: one row per (account, node) the account
-- currently has a peer on, each with its own IP allocated from that node's own
-- independent subnet (no cross-node collision risk - see internal/ipalloc).
--
-- Deliberately NO UNIQUE(node_id, assigned_ip): SoftDeleteAccount never clears
-- assigned_ip (it starts a 24h reuse hold instead - see ip_release_at on accounts),
-- so a real DB constraint here would break IP reuse the same way it already doesn't
-- exist as a constraint on the old accounts.assigned_ip today. Availability is
-- checked via a query (status/ip_release_at), same as it always has been.
CREATE TABLE account_peers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    node_id      UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    assigned_ip  TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, node_id)
);

CREATE INDEX idx_account_peers_account_id ON account_peers (account_id);
CREATE INDEX idx_account_peers_node_id ON account_peers (node_id);

-- Backfill every existing single-node account into its first (only) peer row. No
-- production data exists yet for this project (quota enforcement is already known
-- inert, no real customers per the roadmap), so doing the backfill and the column
-- drop in one migration is acceptable here - a real production rollout would split
-- this into create+backfill, verify, then drop in a later release.
INSERT INTO account_peers (account_id, node_id, assigned_ip)
SELECT id, node_id, assigned_ip FROM accounts;

ALTER TABLE accounts DROP COLUMN node_id;
ALTER TABLE accounts DROP COLUMN assigned_ip;
