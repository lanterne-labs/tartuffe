-- +goose Up
-- The explanation of a contradiction, as the site shows it: written for a
-- reader who has only the two facts, from the facts, their source quotes and
-- their dates (contradiction.RunExplain). reason stays the judge's own line.
-- explanation_hash names the prompt version and the two facts the text was
-- written from: another hash means the text is rewritten.
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS explanation TEXT;
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS explanation_en TEXT;
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS explanation_hash TEXT;

-- +goose Down
ALTER TABLE contradictions DROP COLUMN IF EXISTS explanation_hash;
ALTER TABLE contradictions DROP COLUMN IF EXISTS explanation_en;
ALTER TABLE contradictions DROP COLUMN IF EXISTS explanation;
