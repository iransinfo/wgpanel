-- A stable per-account subscription token: GET /api/v1/sub/{token} serves the
-- account's current wg-quick config without panel credentials - the token itself is
-- the capability, same trust level as the .conf contents it returns (which already
-- embed the private key). Stored plaintext (not hashed) deliberately: the panel must
-- be able to re-display the URL, and anyone who can read this column can already read
-- private_key_encrypted's decryption path anyway.
--
-- gen_random_bytes needs pgcrypto (gen_random_uuid alone is core since PG13).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE accounts ADD COLUMN subscription_token TEXT;
UPDATE accounts SET subscription_token = encode(gen_random_bytes(24), 'hex');
ALTER TABLE accounts ALTER COLUMN subscription_token SET NOT NULL;
CREATE UNIQUE INDEX idx_accounts_subscription_token ON accounts (subscription_token);
