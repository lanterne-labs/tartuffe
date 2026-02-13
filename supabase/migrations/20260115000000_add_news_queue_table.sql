-- +goose Up
-- Create news_queue table for tracking persons to search for news
-- Person IDs reference existing persons in the database
-- Queue ordering is deterministic (by person_id ASC)

CREATE TABLE IF NOT EXISTS news_queue (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL UNIQUE REFERENCES persons(id) ON DELETE CASCADE,
    added_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    notes TEXT
);

-- Index for efficient person_id lookups
CREATE INDEX IF NOT EXISTS idx_news_queue_person_id ON news_queue(person_id);

-- Index for querying by added_at (useful for monitoring/debugging)
CREATE INDEX IF NOT EXISTS idx_news_queue_added_at ON news_queue(added_at);
