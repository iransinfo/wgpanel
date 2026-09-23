-- The DNS server(s) written into every generated wg-quick client config. Full-tunnel
-- clients (AllowedIPs = 0.0.0.0/0) can't reach their original resolver once connected,
-- so the config must name one - but the right resolver depends on where the exit node
-- actually egresses. The previous hardcoded "1.1.1.1, 1.0.0.1" is correct for a node
-- with unrestricted egress, but on a network that filters Cloudflare/Google DNS (a
-- real case that produces "connected, but no internet" - the tunnel works, name
-- resolution silently doesn't) an operator needs to point clients at a resolver that
-- actually answers. Comma-separated, matching wg-quick's DNS = syntax; default keeps
-- the prior behavior so existing deployments are unchanged.
ALTER TABLE panel_settings ADD COLUMN client_dns TEXT NOT NULL DEFAULT '1.1.1.1, 1.0.0.1';
