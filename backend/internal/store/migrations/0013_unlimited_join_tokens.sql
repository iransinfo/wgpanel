-- Optional "unlimited" join tokens: reusable and non-expiring, for re-registering
-- an existing node's agent (replacing hardware, rebuilding a container, rotating
-- identity) without the single-use/pending-only redemption this table's default
-- flow requires. Off by default - a leaked unlimited token lets anyone register as
-- that node indefinitely, so generating one is an explicit, logged choice
-- (docs/STORY-10-monitoring-and-domain-management.md follow-up), never the default.
ALTER TABLE nodes ADD COLUMN join_token_unlimited BOOLEAN NOT NULL DEFAULT false;
