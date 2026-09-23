CREATE TABLE admins (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'super_admin',
    totp_secret   TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
