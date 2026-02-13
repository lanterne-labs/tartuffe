-- Delete all news-based facts while keeping law voting facts
-- This script removes facts with source_type = 'news' or without law_key metadata

BEGIN;

-- First, let's see what we're about to delete (for safety)
SELECT
    'News facts to delete:' as info,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count,
    COUNT(DISTINCT fm.id) as mention_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
LEFT JOIN fact_mentions fm ON fs.id = fm.fact_source_id
WHERE fs.source_type = 'news'
   OR fs.source_type IS NULL
   OR (fs.metadata IS NULL OR NOT (fs.metadata ? 'law_key'));

-- Show counts by source type
SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
GROUP BY fs.source_type
ORDER BY fact_count DESC;

-- Now delete news facts
-- The CASCADE in the foreign key will handle fact_sources, fact_mentions, and contradictions
DELETE FROM facts
WHERE id IN (
    SELECT DISTINCT f.id
    FROM facts f
    JOIN fact_sources fs ON f.id = fs.fact_id
    WHERE fs.source_type = 'news'
       OR fs.source_type IS NULL
       OR (fs.metadata IS NULL OR NOT (fs.metadata ? 'law_key'))
);

-- Show remaining counts
SELECT
    'After deletion:' as info,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id;

SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    COUNT(DISTINCT f.id) as fact_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
GROUP BY fs.source_type;

COMMIT;

-- To rollback instead of commit, replace COMMIT with ROLLBACK
