-- +goose Up
-- The real cost of the Claude API per UTC day and phase of the worker
-- (extraction, maintenance), to tune LLM_EXTRACTION_SHARE. llm_api_spend stays
-- the ledger of the cap: it also holds the reservations of calls in flight.
CREATE TABLE IF NOT EXISTS llm_api_spend_phase (
    day DATE NOT NULL,
    phase TEXT NOT NULL,
    spent_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (day, phase)
);

-- +goose Down
DROP TABLE IF EXISTS llm_api_spend_phase;
