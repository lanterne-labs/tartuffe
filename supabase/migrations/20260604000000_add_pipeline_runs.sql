-- +goose Up
-- Per-run summary of the daily pipeline (`dm pipeline daily`). One row is written
-- at the end of every run so Grafana (Postgres datasource) can graph pipeline
-- output over time — the cron process is ephemeral, so Prometheus cannot scrape
-- it; this durable table is the source of truth for pipeline-run history.
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id                   BIGSERIAL PRIMARY KEY,
    started_at           TIMESTAMPTZ NOT NULL,
    finished_at          TIMESTAMPTZ NOT NULL,
    duration_seconds     INTEGER     NOT NULL,
    status               TEXT        NOT NULL, -- 'success' | 'warning' | 'no_content' | 'error'
    targets              INTEGER     NOT NULL DEFAULT 0,
    news_articles        INTEGER     NOT NULL DEFAULT 0,
    scrape_errors        INTEGER     NOT NULL DEFAULT 0,
    facts_extracted      INTEGER     NOT NULL DEFAULT 0, -- facts created during the run window
    contradictions_found INTEGER     NOT NULL DEFAULT 0, -- contradictions created during the run window
    persons_updated      INTEGER     NOT NULL DEFAULT 0, -- distinct persons emitting person_updated in the window
    audit_checked        INTEGER     NOT NULL DEFAULT 0,
    audit_flagged        INTEGER     NOT NULL DEFAULT 0,
    step_errors          INTEGER     NOT NULL DEFAULT 0,
    error_summary        TEXT
);

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_started_at ON pipeline_runs (started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS pipeline_runs;
