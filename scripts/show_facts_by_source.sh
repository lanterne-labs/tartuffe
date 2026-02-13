#!/bin/bash
# Show facts grouped by source type

set -e

# Get database URL from environment or supabase status
if [ -z "$DATABASE_URL" ]; then
    export DATABASE_URL=$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//')
fi

echo "=== Facts by Source Type ==="
psql "$DATABASE_URL" -c "
SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count,
    COUNT(DISTINCT fm.person_id) as people_mentioned
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
LEFT JOIN fact_mentions fm ON fs.id = fm.fact_source_id
GROUP BY fs.source_type
ORDER BY fact_count DESC;
"

echo ""
echo "=== Sample Facts by Type ==="
psql "$DATABASE_URL" -c "
SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    LEFT(f.fact_text, 80) as sample_fact,
    fs.metadata->>'law_key' as law_key
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
ORDER BY fs.source_type, f.id
LIMIT 10;
"
