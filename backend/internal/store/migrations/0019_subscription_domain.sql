-- A separate public domain (and optionally port) for the subscription capability
-- URLs (GET /api/v1/sub/{token}), distinct from panel_domain. The panel domain
-- already serves subscription URLs through its /api/* proxy block, so this exists
-- for operators who want the links they hand to end users to not reveal (or share
-- fate with) the admin panel's domain: Caddy gets a second site block that serves
-- ONLY /api/v1/sub/* on this domain and 404s everything else, with its own
-- automatically-provisioned certificate (see internal/caddyadmin).
--
-- NULL sub_domain means the feature is off (subscription URLs stay on the panel's
-- origin, exactly the pre-0019 behavior). NULL sub_port means the default 443 -
-- stored as NULL rather than 443 so "explicitly chosen" and "never set" stay
-- distinguishable, matching how panel_domain treats NULL (migration 0012).
ALTER TABLE panel_settings ADD COLUMN sub_domain TEXT;
ALTER TABLE panel_settings ADD COLUMN sub_port INTEGER;
