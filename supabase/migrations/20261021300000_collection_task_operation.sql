-- +goose Up
-- The operation a collection task belongs to (dm collect add --operation), so
-- a batch of tasks such as a two-year backfill is followed as one whole
-- (Grafana, Operations).
ALTER TABLE collection_tasks ADD COLUMN IF NOT EXISTS operation TEXT;
CREATE INDEX IF NOT EXISTS idx_collection_tasks_operation ON collection_tasks(operation) WHERE operation IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_collection_tasks_operation;
ALTER TABLE collection_tasks DROP COLUMN IF EXISTS operation;
