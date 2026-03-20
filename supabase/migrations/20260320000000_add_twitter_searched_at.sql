-- +goose Up
-- Track when Twitter handle discovery last searched for a person,
-- so we don't re-search persons who were already checked recently.
ALTER TABLE persons ADD COLUMN IF NOT EXISTS twitter_searched_at TIMESTAMPTZ;
