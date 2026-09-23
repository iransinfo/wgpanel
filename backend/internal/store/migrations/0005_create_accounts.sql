-- docs/PRD-account-management.md §4. `owner_key_namespace` is deliberately omitted -
-- there's no bot API-key system yet to scope it to (a later story); add it then.
CREATE TABLE accounts (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    external_ref           TEXT UNIQUE,
    label                  TEXT NOT NULL,
    node_id                UUID NOT NULL REFERENCES nodes(id),
    public_key             TEXT NOT NULL,
    private_key_encrypted  TEXT NOT NULL,
    assigned_ip            TEXT NOT NULL,
    data_quota_bytes       BIGINT,           -- NULL = unlimited
    data_used_bytes        BIGINT NOT NULL DEFAULT 0,
    expiry_at              TIMESTAMPTZ,      -- NULL = never expires
    device_limit           INTEGER,          -- NULL = unlimited, soft-enforced (PRD §6.4, not built yet)
    status                 TEXT NOT NULL DEFAULT 'active', -- active | suspended | deleted (see STORY-03 scope note on 'pending')
    suspend_reason         TEXT,             -- quota_exceeded | expired | manual | abuse_flag | NULL
    ip_release_at          TIMESTAMPTZ,      -- set on delete; IP isn't reusable until this passes (24h hold, PRD §6.1)
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_accounts_node_id ON accounts (node_id);
CREATE INDEX idx_accounts_status ON accounts (status);
