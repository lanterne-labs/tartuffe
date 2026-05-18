-- +goose Up
-- Add lang column to facts to track the language of each fact.
-- Used by stance_manager to determine the majority language when generating
-- user-visible text (summaries, labels, contradiction explanations).
ALTER TABLE facts ADD COLUMN IF NOT EXISTS lang TEXT;

-- +goose Down
ALTER TABLE facts DROP COLUMN IF EXISTS lang;
