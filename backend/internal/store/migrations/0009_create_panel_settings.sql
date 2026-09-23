-- Panel-wide configuration, editable from the admin panel's Settings screen instead of
-- only through deploy/.env (which requires a redeploy to change). Deliberately a
-- singleton row (id fixed at 1, enforced by the CHECK) rather than a generic key/value
-- table - every setting here has a known type and a sensible default, and the set of
-- settings is small enough that a real table with real columns beats an untyped
-- string-keyed store for this project's needs.
CREATE TABLE panel_settings (
    id                     INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    public_base_url        TEXT,    -- e.g. "https://panel.example.com" - shown in the UI (e.g. next to
                                     -- join-token/config instructions) and reserved for future outbound
                                     -- links (QR codes, shareable URLs). Purely informational today -
                                     -- changing it here does not move TLS/DNS, which still lives in
                                     -- deploy/.env's PANEL_DOMAIN and requires a real redeploy.
    default_data_quota_gb  NUMERIC, -- pre-fills the "New account" dialog's quota field; NULL = no default (unlimited)
    default_device_limit  INTEGER, -- pre-fills the "New account" dialog's device-limit field; NULL = no default (unlimited)
    default_node_capacity INTEGER NOT NULL DEFAULT 250, -- pre-fills the "New node" dialog's capacity field
    support_contact        TEXT,    -- e.g. a support email/Telegram handle, shown to admins who need help
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO panel_settings (id) VALUES (1);
