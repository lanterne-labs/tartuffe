-- +goose Up
-- Add processed_laws table to track fully processed laws
-- This enables resumable processing and prevents data loss from partial law processing

CREATE TABLE processed_laws (
    id BIGSERIAL PRIMARY KEY,
    law_key TEXT NOT NULL UNIQUE,
    legislature INT NOT NULL,
    title TEXT NOT NULL,
    facts_count INT NOT NULL,  -- For debugging/verification
    processed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT processed_laws_law_key_unique UNIQUE (law_key)
);

CREATE INDEX idx_processed_laws_legislature ON processed_laws(legislature);
CREATE INDEX idx_processed_laws_processed_at ON processed_laws(processed_at);

COMMENT ON TABLE processed_laws IS
'Tracks laws that have been fully processed (all facts stored).
Used to prevent reprocessing and ensure resumability on failures.';
