-- +goose Up
-- Allow facts.embedding to be NULL so that embedding-model swaps can wipe and
-- backfill via `dm embeddings invalidate` + `dm embeddings regenerate`. Keeping
-- the column NOT NULL forced a schema rewrite even when the new model produced
-- the same dimension, which made re-embeddings unnecessarily destructive.
ALTER TABLE facts ALTER COLUMN embedding DROP NOT NULL;
