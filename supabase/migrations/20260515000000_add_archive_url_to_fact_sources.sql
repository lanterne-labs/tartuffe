-- +goose Up
ALTER TABLE fact_sources ADD COLUMN IF NOT EXISTS archive_url TEXT;
ALTER TABLE fact_sources ADD COLUMN IF NOT EXISTS archive_attempted_at TIMESTAMPTZ;
