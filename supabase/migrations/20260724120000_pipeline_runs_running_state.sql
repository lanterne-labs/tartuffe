-- +goose Up
-- A pipeline_runs row is now INSERTed at run START (status='running',
-- finished_at NULL) and completed by UPDATE at run end, so killed/hung runs
-- leave a visible trace instead of vanishing (mirror-bp0: the Jul 22+23 runs
-- died without any record). finished_at/duration therefore become nullable.
ALTER TABLE pipeline_runs ALTER COLUMN finished_at DROP NOT NULL;
ALTER TABLE pipeline_runs ALTER COLUMN duration_seconds DROP NOT NULL;

-- +goose Down
UPDATE pipeline_runs SET finished_at = started_at, duration_seconds = 0 WHERE finished_at IS NULL;
ALTER TABLE pipeline_runs ALTER COLUMN finished_at SET NOT NULL;
ALTER TABLE pipeline_runs ALTER COLUMN duration_seconds SET NOT NULL;
