-- Live-managed TLS domain (docs/STORY-10-monitoring-and-domain-management.md, Part 2),
-- distinct from panel_settings.public_base_url which migration 0009 documents as
-- purely informational. NULL means "never set via the panel" - Caddy is still
-- running whatever PANEL_DOMAIN its container was started with.
ALTER TABLE panel_settings ADD COLUMN panel_domain TEXT;
