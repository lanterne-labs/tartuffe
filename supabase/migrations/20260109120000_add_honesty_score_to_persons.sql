-- +goose Up
-- Add honesty_score column to persons table
-- This column represents the computed honesty score for each person
-- Score ranges from 0.0 (completely dishonest) to 1.0 (completely honest)

ALTER TABLE persons
ADD COLUMN IF NOT EXISTS honesty_score DOUBLE PRECISION NOT NULL DEFAULT 1.0;

-- Add constraint to ensure honesty_score is between 0.0 and 1.0
DO $$
BEGIN
    ALTER TABLE persons
    ADD CONSTRAINT honesty_score_range CHECK (honesty_score >= 0.0 AND honesty_score <= 1.0);
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;