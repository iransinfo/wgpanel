-- Per-account bandwidth (rate) limit in Mbps, enforced by the node agent via tc
-- (HTB egress shaping + ingress policing on the WireGuard interface). NULL =
-- unlimited. This is the "rate" sibling of data_quota_bytes' "volume" cap.
ALTER TABLE accounts ADD COLUMN bandwidth_limit_mbps INTEGER;
