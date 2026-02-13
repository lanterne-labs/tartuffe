-- +goose Up
-- Add news_search_log table for tracking news search history per person/party
-- This enables smart prioritization: skip persons with no prior results, focus on high-coverage targets

CREATE TABLE IF NOT EXISTS news_search_log (
    id BIGSERIAL PRIMARY KEY,
    -- Reference to the person being searched
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    -- When the search was performed
    searched_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Number of search results returned (0 means no coverage found)
    result_count INT NOT NULL DEFAULT 0,
    -- Number of facts extracted from the search
    facts_extracted INT NOT NULL DEFAULT 0,
    -- Search query used (for debugging/audit)
    query TEXT,
    -- Optional: date range used for the search
    date_from DATE,
    date_to DATE
);

-- Index for efficient lookup by person
CREATE INDEX IF NOT EXISTS idx_news_search_log_person_id ON news_search_log(person_id);

-- Index for efficient lookup by searched_at (for recency-based prioritization)
CREATE INDEX IF NOT EXISTS idx_news_search_log_searched_at ON news_search_log(searched_at);

-- Composite index for prioritization queries: find persons with results in last N days
CREATE INDEX IF NOT EXISTS idx_news_search_log_person_results ON news_search_log(person_id, searched_at, result_count);
