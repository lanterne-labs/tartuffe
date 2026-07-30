-- +goose Up
-- Queue of pipeline runs. Every pipeline execution (the nightly one included) is
-- a row here: the cron service only ENQUEUES, and a single long-lived worker
-- (`dm pipeline worker`) claims one job at a time and runs it. `config` is the
-- serialized cmd.PipelineConfig (targets, date window, enabled steps), so a job
-- can be scoped to "Macron, news of 2017-05" without a second pipeline codepath.
CREATE TABLE IF NOT EXISTS pipeline_jobs (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT        NOT NULL,
    config       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status       TEXT        NOT NULL DEFAULT 'queued', -- 'queued'|'running'|'success'|'error'|'canceled'
    priority     INTEGER     NOT NULL DEFAULT 100,      -- lower runs first
    attempts     INTEGER     NOT NULL DEFAULT 0,
    max_attempts INTEGER     NOT NULL DEFAULT 3,
    worker_id    TEXT,
    queued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    run_id       BIGINT REFERENCES pipeline_runs (id) ON DELETE SET NULL,
    error        TEXT
);

-- One active job per name: re-enqueueing 'daily' while yesterday's run is still
-- going is a no-op instead of stacking a second 3-hour run. Terminal rows are
-- excluded so the same name can be run again later.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_jobs_active_name
    ON pipeline_jobs (name)
    WHERE status IN ('queued', 'running');

-- Claim path: ORDER BY priority, queued_at over the queued rows.
CREATE INDEX IF NOT EXISTS idx_pipeline_jobs_claim
    ON pipeline_jobs (status, priority, queued_at);

-- Which job produced a pipeline_runs summary. Needed so the watchdog ("has the
-- nightly run completed recently?") and the Grafana history are not satisfied by
-- an ad-hoc target-scoped backfill run.
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS job_name TEXT NOT NULL DEFAULT 'daily';

-- +goose Down
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS job_name;
DROP INDEX IF EXISTS idx_pipeline_jobs_claim;
DROP INDEX IF EXISTS idx_pipeline_jobs_active_name;
DROP TABLE IF EXISTS pipeline_jobs;
