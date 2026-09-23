-- NOTE: docs/PRD-security-access-control.md §9 calls for the app's DB role to have no
-- UPDATE/DELETE grants on this table. Table owners always bypass GRANT restrictions in
-- Postgres, and this walking-skeleton story connects as the schema-owning role for
-- simplicity, so that protection isn't truly enforced yet - it needs a second, non-owner
-- runtime role. Tracked as a follow-up hardening task, not silently claimed as done here.
CREATE TABLE audit_log (
    id         BIGSERIAL PRIMARY KEY,
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    target     TEXT,
    detail     JSONB,
    ip_address TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_actor ON audit_log (actor);
CREATE INDEX idx_audit_log_created_at ON audit_log (created_at);
