-- +goose Up
-- Wayback Machine archival of source URLs. archive_url holds the snapshot URL once
-- saved; archive_attempted_at records the last archiving attempt so the background
-- worker does not retry within its cooldown window even when no snapshot was produced.
ALTER TABLE fact_sources ADD COLUMN IF NOT EXISTS archive_url TEXT;
ALTER TABLE fact_sources ADD COLUMN IF NOT EXISTS archive_attempted_at TIMESTAMPTZ;

-- Partial index over the worker's selection predicate (sources still missing a snapshot).
CREATE INDEX IF NOT EXISTS idx_fact_sources_needs_archive
    ON fact_sources (archive_attempted_at)
    WHERE archive_url IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_fact_sources_needs_archive;
ALTER TABLE fact_sources DROP COLUMN IF EXISTS archive_attempted_at;
ALTER TABLE fact_sources DROP COLUMN IF EXISTS archive_url;
