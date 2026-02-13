-- +goose Up
-- Add source_type and metadata columns to fact_sources for plugin observability
-- Safe to run multiple times if columns already exist (use IF NOT EXISTS where supported)

ALTER TABLE fact_sources
    ADD COLUMN IF NOT EXISTS source_type TEXT;

ALTER TABLE fact_sources
    ADD COLUMN IF NOT EXISTS metadata JSONB;

-- Optional index for querying by source_type (observability/debugging)
CREATE INDEX IF NOT EXISTS idx_fact_sources_source_type ON fact_sources(source_type);

