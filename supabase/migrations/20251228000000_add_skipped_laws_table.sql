-- +goose Up
CREATE TABLE skipped_laws (
    id BIGSERIAL PRIMARY KEY,
    law_key TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    legislature INT NOT NULL,
    vote_date DATE,
    skip_reason TEXT NOT NULL,  -- 'keyword_match' | 'low_ai_confidence'
    matched_pattern TEXT,        -- e.g., 'amendement n°' if keyword-based
    ai_confidence REAL,          -- NULL if keyword-based, 0-1 if AI-based
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_skipped_laws_legislature ON skipped_laws(legislature);
CREATE INDEX idx_skipped_laws_reason ON skipped_laws(skip_reason);
