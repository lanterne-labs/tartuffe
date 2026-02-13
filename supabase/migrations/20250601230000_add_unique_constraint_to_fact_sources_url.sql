-- +goose Up
ALTER TABLE fact_sources
ADD CONSTRAINT fact_sources_source_url_unique UNIQUE (source_url); 