-- Enabled up front even though the first tables that need it (peer traffic
-- hypertables, per docs/PRD-monitoring-stats.md) don't exist yet in this walking-skeleton
-- story - avoids a second migration fighting over extension ownership later.
CREATE EXTENSION IF NOT EXISTS timescaledb;
