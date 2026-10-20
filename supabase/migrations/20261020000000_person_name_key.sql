-- +goose Up
-- person_name_key folds a name like util.PersonNameKey: lowercase, unaccented,
-- every separator removed (spaces, ASCII punctuation and symbols, the soft
-- hyphen, the zero-width marks). "olivier fa-ure" and "olivier faure" share a
-- key, so the insert paths resolve the first to the second.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION person_name_key(text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
SET search_path = public, extensions, pg_catalog
AS $func$ SELECT regexp_replace(lower(immutable_unaccent($1)), '[\s!-/:-@\[-`{-~­​-‏]', '', 'g') $func$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_persons_name_key ON persons (person_name_key(name));

-- +goose Down
DROP INDEX IF EXISTS idx_persons_name_key;
DROP FUNCTION IF EXISTS person_name_key(text);
