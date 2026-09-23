-- docs/STORY-05-bot-api-key-auth.md: bot/reseller API keys, scoped to a node-group
-- set and a permission set. secret_encrypted (not hashed) because HMAC verification
-- needs the raw secret recoverable server-side. previous_secret_* supports zero-
-- downtime rotation (PRD-telegram-bot-api.md §5.2's 24h grace period).
CREATE TABLE api_keys (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_id                    TEXT NOT NULL UNIQUE,
    secret_encrypted          TEXT NOT NULL,
    previous_secret_encrypted TEXT,
    previous_secret_expires_at TIMESTAMPTZ,
    label                     TEXT NOT NULL,
    node_groups               TEXT[] NOT NULL DEFAULT '{}', -- empty = no nodes allowed, not "all nodes"
    permissions               TEXT[] NOT NULL DEFAULT '{}', -- read, create, update, suspend, delete
    revoked_at                TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- STORY-03 deliberately omitted this column ("no bot API-key system yet to scope it
-- to"); this is that system landing.
ALTER TABLE accounts ADD COLUMN owner_key_namespace TEXT;
CREATE INDEX idx_accounts_owner_key_namespace ON accounts (owner_key_namespace);
