-- Add fact_text_en column to facts to store the English translation of
-- non-English facts (alongside the native fact_text in the source language).
-- NULL when the source is already English.
ALTER TABLE facts ADD COLUMN IF NOT EXISTS fact_text_en TEXT;
COMMENT ON COLUMN facts.fact_text_en IS 'English translation of fact_text. NULL when source is already English.';
