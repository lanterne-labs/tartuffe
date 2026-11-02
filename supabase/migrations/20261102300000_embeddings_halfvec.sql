-- +goose Up
-- Store the embeddings in half precision: 2 bytes per dimension instead of 4,
-- so a 4096-dim embedding takes 8 KB instead of 16 KB. Cosine distances move
-- by about 1e-3, far below the thresholds the grouping and the detection use,
-- so nothing judged before needs judging again. The code reads and writes
-- pgvector.HalfVector. The column rewrite needs free space about the size of
-- the facts table.
ALTER TABLE facts            ALTER COLUMN embedding         TYPE halfvec(4096) USING embedding::halfvec(4096);
ALTER TABLE stance_documents ALTER COLUMN cluster_embedding TYPE halfvec(4096) USING cluster_embedding::halfvec(4096);

-- +goose Down
ALTER TABLE facts            ALTER COLUMN embedding         TYPE vector(4096) USING embedding::vector(4096);
ALTER TABLE stance_documents ALTER COLUMN cluster_embedding TYPE vector(4096) USING cluster_embedding::vector(4096);
