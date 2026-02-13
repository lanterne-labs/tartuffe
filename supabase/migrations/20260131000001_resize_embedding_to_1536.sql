-- +goose Up
-- Resize embedding column from 1024 to 1536 dimensions (model change)
-- Drop the old index first, then alter the column, then recreate the index
DROP INDEX IF EXISTS facts_embedding_idx;

ALTER TABLE facts
    ALTER COLUMN embedding TYPE vector(1536);

CREATE INDEX ON facts USING hnsw (embedding vector_cosine_ops);
