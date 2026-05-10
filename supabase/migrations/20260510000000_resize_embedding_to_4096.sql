-- +goose Up
-- Switch embedding model to qwen3-embedding-8b (4096-dim).
--
-- pgvector 0.8.0 cannot ALTER COLUMN TYPE between different vector dimensions
-- while data is present, so this migration wipes existing embeddings and the
-- stance_documents derived from them. Re-populate after deploy:
--   1. dm contradictions delete --all
--   2. dm embeddings regenerate     (refills facts.embedding via the embedder)
--   3. dm stances rebuild --parallel 8
--   4. dm scores recompute
--
-- HNSW indexes are dropped and NOT recreated: pgvector 0.8.0 caps HNSW at 2000
-- dimensions for plain `vector` (4000 for `halfvec`), and 4096 exceeds both. The
-- only KNN consumer in the rebuild path is the Bundler's per-person stance
-- lookup, which falls back to sequential scan over a tiny per-person stance
-- set — fine without an index.

-- 1. Drop the vector index — required before changing column dimension.
DROP INDEX IF EXISTS facts_embedding_hnsw_idx;
DROP INDEX IF EXISTS facts_embedding_idx;

-- 2. Wipe existing 1536-d embeddings; pgvector cannot ALTER TYPE across dims.
UPDATE facts SET embedding = NULL WHERE embedding IS NOT NULL;

-- 3. Delete stance_documents. CASCADE handles stance_document_facts; the
--    contradictions.bundle_stance_id FK is ON DELETE SET NULL so contradictions
--    survive but become unbundled (re-bundled by `dm stances rebuild`).
DELETE FROM stance_documents;

-- 4. Resize the columns.
ALTER TABLE facts            ALTER COLUMN embedding         TYPE vector(4096);
ALTER TABLE stance_documents ALTER COLUMN cluster_embedding TYPE vector(4096);
