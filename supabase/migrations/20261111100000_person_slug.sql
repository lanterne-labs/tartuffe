-- +goose Up
-- A person's slug names their page (/person/marine-le-pen) instead of their
-- id. It is set once, from the name at the insert, and never changes: a
-- rename keeps it, so no shared link dies. The database makes it (the
-- trigger below), so every insert path (the CLI, the plugins) gets one and
-- Go never computes a slug of its own.
ALTER TABLE persons ADD COLUMN IF NOT EXISTS slug TEXT;

-- person_slug_base folds a name to its slug: lowercase, unaccented, every
-- run of other characters a hyphen ("Jean-Luc Mélenchon" -> jean-luc-melenchon).
-- A slug made of digits only would read as an id in the API's /person/{id}:
-- it gets a letter in front. A name without a letter or a digit of the
-- Latin alphabet gives '' and the person is named by their id (p-<id>).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION person_slug_base(text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
SET search_path = public, extensions, pg_catalog
AS $func$
  SELECT CASE WHEN s ~ '^[0-9]+$' THEN 'p-' || s ELSE s END
  FROM (SELECT trim(both '-' from regexp_replace(lower(immutable_unaccent($1)), '[^a-z0-9]+', '-', 'g')) AS s) t
$func$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION persons_set_slug()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
DECLARE
  base text;
  candidate text;
  n int := 1;
BEGIN
  IF NEW.slug IS NOT NULL AND NEW.slug <> '' THEN
    RETURN NEW;
  END IF;
  base := person_slug_base(NEW.name);
  IF base = '' THEN
    base := 'p-' || NEW.id;
  END IF;
  candidate := base;
  WHILE EXISTS (SELECT 1 FROM persons WHERE slug = candidate AND id IS DISTINCT FROM NEW.id) LOOP
    n := n + 1;
    candidate := base || '-' || n;
  END LOOP;
  NEW.slug := candidate;
  RETURN NEW;
END
$func$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS persons_slug ON persons;
CREATE TRIGGER persons_slug BEFORE INSERT ON persons
FOR EACH ROW EXECUTE FUNCTION persons_set_slug();

-- The persons already there: the oldest of a shared base keeps it bare.
WITH based AS (
  SELECT id, COALESCE(NULLIF(person_slug_base(name), ''), 'p-' || id) AS base
  FROM persons WHERE slug IS NULL
), ranked AS (
  SELECT id, base, row_number() OVER (PARTITION BY base ORDER BY id) AS n FROM based
)
UPDATE persons p
SET slug = CASE WHEN r.n = 1 THEN r.base ELSE r.base || '-' || r.n END
FROM ranked r WHERE p.id = r.id;

ALTER TABLE persons ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS persons_slug_key ON persons (slug);

-- +goose Down
DROP TRIGGER IF EXISTS persons_slug ON persons;
DROP FUNCTION IF EXISTS persons_set_slug();
DROP FUNCTION IF EXISTS person_slug_base(text);
DROP INDEX IF EXISTS persons_slug_key;
ALTER TABLE persons DROP COLUMN IF EXISTS slug;
