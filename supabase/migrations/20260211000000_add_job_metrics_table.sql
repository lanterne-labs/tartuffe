-- +goose Up
CREATE TABLE job_metrics (
    id BIGSERIAL PRIMARY KEY,
    job_type TEXT NOT NULL,
    target TEXT,
    metric TEXT NOT NULL,
    value DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_job_metrics_job_type ON job_metrics (job_type);
CREATE INDEX idx_job_metrics_created_at ON job_metrics (created_at);
CREATE INDEX idx_job_metrics_metric ON job_metrics (metric);

-- +goose Down
DROP TABLE IF EXISTS job_metrics;
