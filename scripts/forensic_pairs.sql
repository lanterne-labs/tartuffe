-- Companion to forensic.sql: dumps the actual fact pairs behind the coverage numbers.
-- Section 1: closest pairs the detector judged NO (false-negative review material).
-- Section 2: pairs just OUTSIDE the KNN threshold, never judged (threshold-gap review material).
-- Run: host run --service postgres -- bash -c 'psql "$DATABASE_PUBLIC_URL" -v targets="macron|melenchon" -f scripts/forensic_pairs.sql'
\pset pager off
\pset format wrapped
\pset columns 220
CREATE TEMP TABLE t_targets AS
SELECT id, name FROM persons WHERE name ~ :'targets';

CREATE TEMP TABLE active AS
WITH pf AS (
    SELECT DISTINCT fm.person_id, fs.fact_id
    FROM fact_mentions fm JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id IN (SELECT id FROM t_targets)
)
SELECT pf.person_id, f.id, f.embedding, f.fact_text
FROM pf JOIN facts f ON f.id = pf.fact_id
WHERE f.embedding IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM fact_relations fr
                  WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate');

\echo '=== CLOSEST JUDGED-NO PAIRS (within threshold, detector said no) ==='
WITH cand AS (
    SELECT a.person_id, a.id AS fa, b.id AS fb, (a.embedding <-> b.embedding) AS dist,
           a.fact_text AS ta, b.fact_text AS tb
    FROM active a JOIN active b ON a.person_id = b.person_id AND a.id < b.id
    WHERE (a.embedding <-> b.embedding) <= 0.8
)
SELECT t.name, round(cand.dist::numeric,3) AS dist, cand.fa, cand.fb,
       left(cand.ta,160) AS fact_a, left(cand.tb,160) AS fact_b
FROM cand
JOIN fact_pair_checks fpc ON fpc.person_id = cand.person_id
     AND fpc.fact_a_id = cand.fa AND fpc.fact_b_id = cand.fb AND NOT fpc.is_contradiction
JOIN t_targets t ON t.id = cand.person_id
ORDER BY t.name, cand.dist LIMIT 40;

\echo '=== NEAR-MISS PAIRS just OUTSIDE the KNN threshold (0.80 < dist <= 0.92, never judged) ==='
WITH cand AS (
    SELECT a.person_id, a.id AS fa, b.id AS fb, (a.embedding <-> b.embedding) AS dist,
           a.fact_text AS ta, b.fact_text AS tb
    FROM active a JOIN active b ON a.person_id = b.person_id AND a.id < b.id
    WHERE (a.embedding <-> b.embedding) > 0.8 AND (a.embedding <-> b.embedding) <= 0.92
)
SELECT t.name, round(cand.dist::numeric,3) AS dist,
       left(cand.ta,160) AS fact_a, left(cand.tb,160) AS fact_b
FROM cand
JOIN t_targets t ON t.id = cand.person_id
LEFT JOIN fact_pair_checks fpc ON fpc.person_id = cand.person_id
     AND fpc.fact_a_id = cand.fa AND fpc.fact_b_id = cand.fb
WHERE fpc.id IS NULL
ORDER BY t.name, cand.dist LIMIT 30;
