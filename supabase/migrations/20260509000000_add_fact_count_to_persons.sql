-- +goose Up
-- Materialize deduped fact_count on persons for O(1) list queries (no JOIN/GROUP BY)
ALTER TABLE persons ADD COLUMN IF NOT EXISTS fact_count INTEGER NOT NULL DEFAULT 0;

-- Backfill: deduped count (excluding facts marked as duplicates in fact_relations)
UPDATE persons p
SET fact_count = (
    SELECT COUNT(DISTINCT fs.fact_id) FILTER (
        WHERE NOT EXISTS (
            SELECT 1 FROM fact_relations fr
            WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate'
        )
    )
    FROM fact_mentions fm
    JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id = p.id
);

-- Index for ORDER BY honesty_score (panel queries: top honest / least honest)
CREATE INDEX IF NOT EXISTS idx_persons_honesty_score ON persons(honesty_score DESC);
