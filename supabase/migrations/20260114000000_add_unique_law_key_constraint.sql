-- +goose Up
-- Add unique constraint on law_key in fact_sources metadata
-- This prevents processing the same law multiple times
-- Uses a partial unique index that only applies when law_key is present in metadata

CREATE UNIQUE INDEX idx_fact_sources_unique_law_key
ON fact_sources ((metadata->>'law_key'))
WHERE metadata->>'law_key' IS NOT NULL;

-- Optional: Add a comment explaining the constraint
COMMENT ON INDEX idx_fact_sources_unique_law_key IS
'Ensures each law (identified by law_key in metadata) is only processed once. Applies only to fact sources that have a law_key in their metadata.';
