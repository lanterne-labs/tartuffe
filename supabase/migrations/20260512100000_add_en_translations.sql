-- +goose Up
-- Add English translation columns for user-visible LLM-generated text.
-- These are NULL when the native text is already in English or when no translation
-- has been generated yet (e.g. for rows created before this migration).
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS reason_en TEXT;
ALTER TABLE stance_documents ADD COLUMN IF NOT EXISTS summary_en TEXT;
ALTER TABLE stance_documents ADD COLUMN IF NOT EXISTS label_en TEXT;

-- +goose Down
ALTER TABLE stance_documents DROP COLUMN IF EXISTS label_en;
ALTER TABLE stance_documents DROP COLUMN IF EXISTS summary_en;
ALTER TABLE contradictions DROP COLUMN IF EXISTS reason_en;
