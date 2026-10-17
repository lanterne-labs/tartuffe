-- +goose Up
-- The nightly cron is gone: the worker (dm worker run) runs the collection
-- tasks and the maintenance pass. The run history of the nightly and the
-- history cursor of the plugins have no reader any more.
DROP TABLE IF EXISTS pipeline_runs;
DROP TABLE IF EXISTS history_cursors;

-- +goose Down
CREATE TABLE IF NOT EXISTS history_cursors (
    plugin TEXT PRIMARY KEY,
    reached_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id BIGSERIAL PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    duration_seconds INT,
    status TEXT NOT NULL,
    targets INT NOT NULL DEFAULT 0,
    news_articles INT NOT NULL DEFAULT 0,
    scrape_errors INT NOT NULL DEFAULT 0,
    facts_extracted INT NOT NULL DEFAULT 0,
    contradictions_found INT NOT NULL DEFAULT 0,
    persons_updated INT NOT NULL DEFAULT 0,
    audit_checked INT NOT NULL DEFAULT 0,
    audit_flagged INT NOT NULL DEFAULT 0,
    step_errors INT NOT NULL DEFAULT 0,
    error_summary TEXT
);
