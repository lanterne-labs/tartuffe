-- +goose Up
-- Add a `lang` column to persons (BCP-47 source language) to drive per-person
-- news search localisation. Defaults to 'fr' for the historical French-politics
-- focus; new persons created from non-French scraping pick up their actual
-- detected language.
--
-- Backfill from the existing facts table: each person's lang is the majority
-- lang across their facts. Persons with no facts (or facts with no lang) keep
-- the 'fr' default.
ALTER TABLE persons ADD COLUMN IF NOT EXISTS lang TEXT NOT NULL DEFAULT 'fr';

-- One-shot backfill: most frequent fact.lang per person.
WITH person_majority AS (
  SELECT
    fm.person_id,
    f.lang AS lang,
    COUNT(*) AS n,
    ROW_NUMBER() OVER (PARTITION BY fm.person_id ORDER BY COUNT(*) DESC, f.lang) AS rn
  FROM fact_mentions fm
  JOIN fact_sources fs ON fs.id = fm.fact_source_id
  JOIN facts f ON f.id = fs.fact_id
  WHERE f.lang IS NOT NULL AND f.lang <> ''
  GROUP BY fm.person_id, f.lang
)
UPDATE persons p
SET lang = pm.lang
FROM person_majority pm
WHERE pm.person_id = p.id AND pm.rn = 1 AND pm.lang IS NOT NULL;

-- +goose Down
ALTER TABLE persons DROP COLUMN IF EXISTS lang;
