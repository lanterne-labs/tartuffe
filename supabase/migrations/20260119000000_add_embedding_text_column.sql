-- +goose Up
-- Add embedding_text column to facts table
-- This column stores a name-free version of the fact text used ONLY for generating embeddings.
-- The original fact_text (with names) is preserved for display and LLM comparison.
-- embedding_text removes person names to ensure facts cluster by topic, not by person.

ALTER TABLE facts ADD COLUMN IF NOT EXISTS embedding_text TEXT;

-- Create index for potential text-based lookups
CREATE INDEX IF NOT EXISTS idx_facts_embedding_text ON facts (embedding_text);

COMMENT ON COLUMN facts.embedding_text IS 'Name-free version of fact text used only for embedding generation. Removes person names to ensure facts cluster by topic, not by person name.';
