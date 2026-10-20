-- +goose Up
-- A contradiction points at the parents of the claim groups (RecordContradiction
-- resolves each fact to its parent). Rows recorded on a member before that move
-- to the parent, their group base with them; a row whose two facts share a
-- parent, or whose parent pair is already recorded, is dropped.
CREATE TEMP TABLE contradiction_parents ON COMMIT DROP AS
SELECT c.id, c.person_id,
       LEAST(COALESCE(ra.general_fact_id, c.fact_a_id), COALESCE(rb.general_fact_id, c.fact_b_id)) AS a,
       GREATEST(COALESCE(ra.general_fact_id, c.fact_a_id), COALESCE(rb.general_fact_id, c.fact_b_id)) AS b
FROM contradictions c
LEFT JOIN fact_relations ra ON ra.specific_fact_id = c.fact_a_id AND ra.relation_type = 'duplicate'
LEFT JOIN fact_relations rb ON rb.specific_fact_id = c.fact_b_id AND rb.relation_type = 'duplicate'
WHERE ra.id IS NOT NULL OR rb.id IS NOT NULL;

DELETE FROM contradictions c
USING contradiction_parents p
WHERE c.id = p.id
  AND (p.a = p.b
       OR EXISTS (SELECT 1 FROM contradictions o
                  WHERE o.person_id = p.person_id AND o.fact_a_id = p.a AND o.fact_b_id = p.b)
       OR EXISTS (SELECT 1 FROM contradiction_parents q
                  WHERE q.person_id = p.person_id AND q.a = p.a AND q.b = p.b AND q.id < p.id));

UPDATE contradictions c
SET fact_a_id = p.a,
    fact_b_id = p.b,
    base_fact_id = COALESCE((SELECT r.general_fact_id FROM fact_relations r
                             WHERE r.specific_fact_id = c.base_fact_id AND r.relation_type = 'duplicate' LIMIT 1),
                            c.base_fact_id)
FROM contradiction_parents p
WHERE c.id = p.id;

-- +goose Down
-- The rows moved to the parents stay there.
SELECT 1;
