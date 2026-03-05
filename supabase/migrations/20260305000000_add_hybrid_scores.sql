-- +goose Up
ALTER TABLE persons ADD COLUMN IF NOT EXISTS own_score DOUBLE PRECISION DEFAULT 1.0;
ALTER TABLE persons ADD COLUMN IF NOT EXISTS members_score DOUBLE PRECISION;
UPDATE persons SET own_score = honesty_score;
