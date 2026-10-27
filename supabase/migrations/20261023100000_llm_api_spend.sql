-- +goose Up
-- The spend of the Claude API per UTC day, for every process that calls it
-- (the worker, each dm run against this database): the daily cap of
-- ClaudeAPIEndpoint is checked against this row (storage.APISpendLedger).
CREATE TABLE IF NOT EXISTS llm_api_spend (
    day DATE PRIMARY KEY,
    spent_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS llm_api_spend;
