-- Optional free-form region label per node (e.g. "eu", "us-east", "de-fra") used by
-- smart node steering: a subscription/steer request carrying ?region= prefers nodes
-- whose region matches before falling back to the load/health score. Empty string =
-- no region assigned (never matches any requested region).
ALTER TABLE nodes ADD COLUMN region TEXT NOT NULL DEFAULT '';
