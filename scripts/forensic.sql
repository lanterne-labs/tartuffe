-- Forensic analysis: why do high-profile persons show 0 contradictions / 100 honesty?
-- Read-only. Run with: host run --service postgres -- bash -c 'psql "$DATABASE_PUBLIC_URL" -v targets="..." -f scripts/forensic.sql'
-- :targets is a regex matched against persons.name (accent-free lowercase), e.g. 'macron|melenchon|bardella|bompard'
\pset pager off
\set QUIET off

\echo '=== [1] TARGET PERSONS: identity + scores + headline counts ==='
CREATE TEMP TABLE t_targets AS
SELECT id, name FROM persons WHERE name ~ :'targets';

SELECT p.id, p.name, round(p.honesty_score::numeric,3) AS honesty,
       round(p.own_score::numeric,3) AS own_score, p.fact_count,
       p.created_at::date AS person_since,
       (SELECT count(*) FROM contradictions c WHERE c.person_id = p.id) AS contradictions,
       (SELECT count(*) FROM fact_pair_checks fpc WHERE fpc.person_id = p.id) AS pairs_checked,
       (SELECT count(*) FROM fact_pair_checks fpc WHERE fpc.person_id = p.id AND fpc.is_contradiction) AS pairs_yes
FROM persons p JOIN t_targets t ON t.id = p.id
ORDER BY p.fact_count DESC;

\echo '=== [2] TARGET FACT CORPUS: volume + time coverage + source mix ==='
-- A contradiction needs facts spread over TIME. If everything is from the last
-- few weeks, there is nothing old to contradict.
WITH pf AS (
    SELECT DISTINCT fm.person_id, fs.fact_id
    FROM fact_mentions fm JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id IN (SELECT id FROM t_targets)
)
SELECT t.name,
       count(*) AS facts,
       count(*) FILTER (WHERE f.embedding IS NULL) AS no_embedding,
       min(fs.published_at)::date AS oldest_pub,
       max(fs.published_at)::date AS newest_pub,
       count(DISTINCT date_trunc('month', fs.published_at)) AS months_covered,
       min(f.created_at)::date AS first_ingested,
       max(f.created_at)::date AS last_ingested
FROM pf JOIN t_targets t ON t.id = pf.person_id
JOIN facts f ON f.id = pf.fact_id
LEFT JOIN fact_sources fs ON fs.fact_id = f.id
GROUP BY t.name ORDER BY facts DESC;

\echo '=== [2b] TARGET FACTS BY SOURCE TYPE ==='
WITH pf AS (
    SELECT DISTINCT fm.person_id, fs.fact_id, fs.source_type
    FROM fact_mentions fm JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id IN (SELECT id FROM t_targets)
)
SELECT t.name, coalesce(pf.source_type,'?') AS source_type, count(DISTINCT pf.fact_id) AS facts
FROM pf JOIN t_targets t ON t.id = pf.person_id
GROUP BY 1,2 ORDER BY 1,3 DESC;

\echo '=== [2c] TARGET FACTS PER MONTH (published_at) — temporal spread ==='
WITH pf AS (
    SELECT DISTINCT fm.person_id, fs.fact_id
    FROM fact_mentions fm JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id IN (SELECT id FROM t_targets)
)
SELECT t.name, date_trunc('month', fs.published_at)::date AS month, count(DISTINCT pf.fact_id) AS facts
FROM pf JOIN t_targets t ON t.id = pf.person_id
JOIN fact_sources fs ON fs.fact_id = pf.fact_id
GROUP BY 1,2 ORDER BY 1,2;

\echo '=== [3] DETECTION COVERAGE: candidate pairs within KNN threshold vs actually judged ==='
-- Recomputes the detector''s own candidate criterion (L2 distance <= 0.8, both
-- facts active/non-duplicate) and checks each candidate pair against
-- fact_pair_checks. unchecked > 0 means the detector has NOT yet judged pairs
-- it WOULD select — detection is behind, not "all pairs judged NO".
-- paraphrase_skip = pairs the same-verb filter silently drops (never judged).
WITH pf AS (
    SELECT DISTINCT fm.person_id, fs.fact_id
    FROM fact_mentions fm JOIN fact_sources fs ON fm.fact_source_id = fs.id
    WHERE fm.person_id IN (SELECT id FROM t_targets)
),
active AS (
    SELECT pf.person_id, f.id, f.embedding, coalesce(f.canonical_verb,'') AS verb
    FROM pf JOIN facts f ON f.id = pf.fact_id
    WHERE f.embedding IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM fact_relations fr
                      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')
),
cand AS (
    SELECT a.person_id, a.id AS fa, b.id AS fb,
           (a.embedding <-> b.embedding) AS dist,
           -- detector's paraphrase skip: similarity (1 - L2 dist) > 0.80 AND same canonical verb
           (a.verb <> '' AND a.verb = b.verb AND (a.embedding <-> b.embedding) < 0.2) AS same_verb_close
    FROM active a JOIN active b ON a.person_id = b.person_id AND a.id < b.id
    WHERE (a.embedding <-> b.embedding) <= 0.8
)
SELECT t.name,
       count(*) AS candidate_pairs,
       count(*) FILTER (WHERE same_verb_close) AS paraphrase_skip,
       count(fpc.id) AS judged,
       count(fpc.id) FILTER (WHERE fpc.is_contradiction) AS judged_yes,
       count(*) FILTER (WHERE fpc.id IS NULL AND NOT same_verb_close) AS unchecked
FROM cand
JOIN t_targets t ON t.id = cand.person_id
LEFT JOIN fact_pair_checks fpc
       ON fpc.person_id = cand.person_id AND fpc.fact_a_id = cand.fa AND fpc.fact_b_id = cand.fb
GROUP BY t.name ORDER BY candidate_pairs DESC;

\echo '=== [4] STANCE FRAGMENTATION: facts spread across stances ==='
-- Contradictions also surface via within-stance analysis at insertion. If facts
-- are scattered 1-2 per stance, that path never sees comparable pairs.
SELECT t.name, count(sd.id) AS stances,
       sum(sd.fact_count) AS stance_facts,
       max(sd.fact_count) AS biggest_stance,
       round(avg(sd.fact_count)::numeric,1) AS avg_facts_per_stance,
       count(*) FILTER (WHERE sd.fact_count = 1) AS singleton_stances
FROM stance_documents sd JOIN t_targets t ON t.id = sd.person_id
GROUP BY t.name ORDER BY stances DESC;

\echo '=== [5] EVICTION TRAIL: contradictions EVER created vs surviving ==='
-- contradiction_added events are never deleted, so ever_created - surviving =
-- rows later removed (auditor eviction, reverify --apply, dedup repoint, manual
-- dismiss). A big "removed" with 0 surviving = the pipeline DID find
-- contradictions for this person and a later judge erased them.
SELECT t.name,
       count(e.id) AS ever_created,
       (SELECT count(*) FROM contradictions c WHERE c.person_id = t.id) AS surviving,
       count(e.id) - (SELECT count(*) FROM contradictions c WHERE c.person_id = t.id) AS removed,
       min(e.created_at)::date AS first_created,
       max(e.created_at)::date AS last_created
FROM t_targets t
LEFT JOIN event_log e ON e.topic = 'contradiction_added' AND e.entity_type = 'person' AND e.entity_id = t.id
GROUP BY t.id, t.name ORDER BY ever_created DESC;

\echo '=== [5b] EVICTION TRAIL (global): top persons by created-then-removed contradictions ==='
SELECT p.name,
       count(e.id) AS ever_created,
       (SELECT count(*) FROM contradictions c WHERE c.person_id = p.id) AS surviving,
       count(e.id) - (SELECT count(*) FROM contradictions c WHERE c.person_id = p.id) AS removed
FROM event_log e JOIN persons p ON p.id = e.entity_id
WHERE e.topic = 'contradiction_added' AND e.entity_type = 'person'
GROUP BY p.id, p.name
ORDER BY removed DESC LIMIT 15;

\echo '=== [5c] TARGET CONTRADICTION HISTORY: every contradiction_added event ==='
SELECT t.name, e.created_at::timestamp(0),
       left(coalesce(e.payload->>'reason', e.payload::text), 100) AS detail
FROM event_log e JOIN t_targets t ON t.id = e.entity_id
WHERE e.topic = 'contradiction_added' AND e.entity_type = 'person'
ORDER BY t.name, e.created_at;

\echo '=== [6] QUEUE MEMBERSHIP: are targets in the daily news/twitter scope? ==='
SELECT t.name,
       (sq.person_id IS NOT NULL) AS in_search_queue,
       sq.added_at::date AS queued_since
FROM t_targets t LEFT JOIN search_queue sq ON sq.person_id = t.id
ORDER BY t.name;

\echo '=== [7] GLOBAL SANITY: does detection produce contradictions for ANYONE? ==='
SELECT (SELECT count(*) FROM contradictions) AS total_contradictions,
       (SELECT count(DISTINCT person_id) FROM contradictions) AS persons_with_contradictions,
       (SELECT count(*) FROM contradictions WHERE created_at > now() - interval '14 days') AS new_last_14d,
       (SELECT count(*) FROM fact_pair_checks WHERE checked_at > now() - interval '14 days') AS pairs_checked_last_14d;

\echo '=== [7b] TOP-15 PERSONS BY FACTS: score vs contradictions (the 100-score wall) ==='
SELECT p.name, p.fact_count, round(p.honesty_score::numeric,3) AS honesty,
       (SELECT count(*) FROM contradictions c WHERE c.person_id = p.id) AS contras,
       (SELECT count(*) FROM fact_pair_checks f WHERE f.person_id = p.id) AS checked
FROM persons p ORDER BY p.fact_count DESC LIMIT 15;

\echo '=== [7c] RECENT CONTRADICTIONS (who DOES get them) ==='
SELECT p.name, c.created_at::date, left(c.reason, 90) AS reason
FROM contradictions c JOIN persons p ON p.id = c.person_id
ORDER BY c.created_at DESC LIMIT 10;

\echo '=== [8] WORKER: recent collection tasks ==='
SELECT t.id, p.name, t.plugin, t.since, t.until, t.status, t.attempts, t.items, t.facts,
       t.started_at::timestamp(0), t.finished_at::timestamp(0), left(coalesce(t.last_error,''), 60) AS last_error
FROM collection_tasks t JOIN persons p ON p.id = t.person_id ORDER BY t.created_at DESC LIMIT 8;

\echo '=== [8b] CONTRADICTION-RELATED JOB METRICS (last runs; audit evictions show here) ==='
SELECT job_type, target, metric, value, created_at::timestamp(0)
FROM job_metrics
WHERE job_type ~ 'contradiction|detect|audit'
ORDER BY created_at DESC LIMIT 30;
