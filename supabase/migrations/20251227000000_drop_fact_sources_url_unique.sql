-- +goose Up
ALTER TABLE fact_sources DROP CONSTRAINT IF EXISTS fact_sources_source_url_unique;

