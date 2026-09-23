-- docs/STORY-04-node-agent-mtls.md: registration issues a certificate whose
-- fingerprint is pinned here, and heartbeats update last_heartbeat_at, which the
-- background sweep (Store.SweepOfflineNodes) reads to detect a node gone silent.
ALTER TABLE nodes ADD COLUMN mtls_cert_fingerprint TEXT;
ALTER TABLE nodes ADD COLUMN last_heartbeat_at TIMESTAMPTZ;
