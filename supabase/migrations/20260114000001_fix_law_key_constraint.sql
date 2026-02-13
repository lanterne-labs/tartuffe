-- +goose Up
-- Drop the overly restrictive unique constraint on law_key
-- A single law generates multiple facts (party facts, deputy facts), each with its own fact_sources record.
-- The application-level check (IsLawKeyProcessed) prevents reprocessing laws.
-- The existing UNIQUE (fact_id, source_url) constraint prevents duplicate fact_sources for the same fact.

DROP INDEX IF EXISTS idx_fact_sources_unique_law_key;

-- Create a non-unique index for fast lookups by law_key (for IsLawKeyProcessed queries)
CREATE INDEX IF NOT EXISTS idx_fact_sources_law_key ON fact_sources ((metadata->>'law_key'))
WHERE metadata->>'law_key' IS NOT NULL;

-- Optional: Add a comment explaining the indexing strategy
COMMENT ON INDEX idx_fact_sources_law_key IS
'Non-unique index on law_key for fast existence checks. Multiple fact_sources records can have the same law_key (one per fact from that law). Application-level check prevents reprocessing laws.';
