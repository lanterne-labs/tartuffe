-- +goose Up
-- Entity dedup support: pg_trgm for fuzzy candidate generation, unaccent +
-- an IMMUTABLE wrapper so insert-time lookups and a functional index can be
-- accent/case-insensitive (kills "éric zemmour" vs "eric zemmour" duplicates).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- +goose StatementBegin
-- The 2-arg unaccent(regdictionary, text) is IMMUTABLE (the 1-arg form is only
-- STABLE), so wrapping it lets us use it in a functional index. The dictionary
-- literal needs an explicit ::regdictionary cast, and the function pins its own
-- search_path so the unaccent dictionary resolves even during index builds
-- (which run with a reset search_path).
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
SET search_path = public, extensions, pg_catalog
AS $func$ SELECT unaccent('unaccent'::regdictionary, $1) $func$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_persons_name_trgm ON persons USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_persons_name_normkey ON persons (lower(immutable_unaccent(name)));

-- +goose Down
DROP INDEX IF EXISTS idx_persons_name_normkey;
DROP INDEX IF EXISTS idx_persons_name_trgm;
DROP FUNCTION IF EXISTS immutable_unaccent(text);
