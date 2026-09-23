-- Discovered while building account config delivery (STORY-03 task 6): a valid
-- wg-quick client config needs the SERVER's WireGuard public key in its [Peer]
-- section, which nothing on the nodes table captured. There's no real node agent yet
-- to report this automatically (PRD-node-management.md §5-6, later story) - for now
-- an admin sets it manually when registering a node (having generated it on the
-- server themselves, e.g. `wg genkey | tee privatekey | wg pubkey`).
ALTER TABLE nodes ADD COLUMN public_key TEXT;
