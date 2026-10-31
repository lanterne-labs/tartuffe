-- The health numbers of one audit. Read-only. Run with:
--   psql "$DBURL" -X -At -F ' | ' -v since="'2026-10-31T00:00:00Z'" -f .claude/skills/audit/scripts/measure.sql
-- since is the start of the window, quoted as an SQL literal (see SKILL.md).

SELECT 'contradictions', count(*),
       count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM contradiction_reviews r WHERE r.contradiction_id = c.id)) AS unreviewed
FROM contradictions c;

SELECT 'last verdict', verdict, count(*)
FROM (SELECT DISTINCT ON (contradiction_id) contradiction_id, verdict
      FROM contradiction_reviews ORDER BY contradiction_id, reviewed_at DESC) x
JOIN contradictions c ON c.id = x.contradiction_id
GROUP BY 2 ORDER BY 2;

SELECT 'unreviewed by person', p.name, count(*)
FROM contradictions c JOIN persons p ON p.id = c.person_id
WHERE NOT EXISTS (SELECT 1 FROM contradiction_reviews r WHERE r.contradiction_id = c.id)
GROUP BY 2 ORDER BY 3 DESC LIMIT 15;

SELECT 'new facts by source', fs.source_type, count(DISTINCT f.id)
FROM facts f JOIN fact_sources fs ON fs.fact_id = f.id
WHERE f.created_at >= :since::timestamptz
GROUP BY 2 ORDER BY 3 DESC;

SELECT 'new entities', entity_type, count(*)
FROM persons WHERE created_at >= :since::timestamptz GROUP BY 2;

SELECT 'new non-human entities', string_agg(name, ', ' ORDER BY name)
FROM persons WHERE created_at >= :since::timestamptz AND entity_type <> 'human';

SELECT 'google redirect sources', count(*)
FROM fact_sources WHERE source_url ~ '^https?://(www\.)?google\.[a-z.]+/(goto|url)';

SELECT 'tweet sources on a profile URL (new)', count(*)
FROM fact_sources WHERE source_type = 'twitter' AND source_url !~ '/status/' AND scraped_at >= :since::timestamptz;

SELECT 'news sources without a date (new)', count(*)
FROM fact_sources WHERE source_type = 'news' AND published_at IS NULL AND scraped_at >= :since::timestamptz;

SELECT 'sources dated before 1995', count(*) FROM fact_sources WHERE published_at < '1995-01-01';

SELECT 'extracted documents without text (new)', source_type, count(*)
FROM source_documents WHERE extracted_at >= :since::timestamptz AND length(content) = 0 GROUP BY 2;

WITH q AS (
  SELECT fm.person_id, lower(regexp_replace(fs.original_quote, '\W+', '', 'g')) k, fs.fact_id
  FROM fact_sources fs JOIN fact_mentions fm ON fm.fact_source_id = fs.id
  WHERE fs.source_type IN ('news', 'twitter', 'viepublique', 'assemblee_debat') AND length(fs.original_quote) > 40),
g AS (SELECT person_id, k, array_agg(DISTINCT fact_id) ids FROM q GROUP BY 1, 2 HAVING count(DISTINCT fact_id) > 1)
SELECT 'same-quote groups with 2+ active facts', count(*),
       sum((SELECT count(*) FROM unnest(g.ids) i
            WHERE NOT EXISTS (SELECT 1 FROM fact_relations r WHERE r.relation_type = 'duplicate' AND r.specific_fact_id = i)) - 1)
FROM g
WHERE (SELECT count(*) FROM unnest(g.ids) i
       WHERE NOT EXISTS (SELECT 1 FROM fact_relations r WHERE r.relation_type = 'duplicate' AND r.specific_fact_id = i)) >= 2;

WITH f AS (SELECT fact_id, count(*) n FROM (SELECT fact_a_id fact_id FROM contradictions UNION ALL SELECT fact_b_id FROM contradictions) x GROUP BY 1)
SELECT 'fan-out: facts in 3+ contradictions', count(*), sum(n) FROM f WHERE n >= 3;

SELECT 'stances by size', CASE WHEN fact_count > 100 THEN '>100' WHEN fact_count >= 20 THEN '20-100' ELSE '<20' END, count(*), sum(fact_count)
FROM stance_documents GROUP BY 2 ORDER BY 2;

SELECT 'api spend', day, round(spent_usd::numeric, 2) FROM llm_api_spend ORDER BY day DESC LIMIT 3;
