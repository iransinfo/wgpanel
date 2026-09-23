-- Control-plane bookkeeping only for now (docs/STORY-02-node-directory-join-tokens.md).
-- mTLS cert fingerprint, heartbeat-driven online/offline status, and load metrics are
-- deferred to the story that builds the actual Go node agent.
CREATE TABLE nodes (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   TEXT NOT NULL UNIQUE,
    node_group             TEXT NOT NULL DEFAULT 'default',
    public_endpoint        TEXT NOT NULL,
    wg_subnet              TEXT NOT NULL,
    capacity_max_peers     INTEGER NOT NULL DEFAULT 250,
    status                 TEXT NOT NULL DEFAULT 'pending', -- pending -> registered (see PRD-node-management.md §5)
    join_token_hash        TEXT,
    join_token_expires_at  TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
