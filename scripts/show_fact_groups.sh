#!/bin/bash
# Show facts and their relations (duplicates and implications)

set -e

if [ -z "$DATABASE_URL" ]; then
    export DATABASE_URL=$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//')
fi

echo "=== Active Facts with Relation Counts ==="
psql "$DATABASE_URL" -c "
SELECT
    f.id,
    LEFT(f.fact_text, 100) as fact_text,
    COUNT(fr.id) as related_count,
    fs.source_type,
    fs.metadata->>'story_token' as story_token
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
LEFT JOIN fact_relations fr ON fr.general_fact_id = f.id
WHERE NOT EXISTS (
    SELECT 1 FROM fact_relations dup
    WHERE dup.specific_fact_id = f.id AND dup.relation_type = 'duplicate'
)
GROUP BY f.id, f.fact_text, fs.source_type, fs.metadata
ORDER BY related_count DESC, f.id DESC
LIMIT 20;
"

echo ""
echo "=== Fact Relations (duplicates and implications) ==="
psql "$DATABASE_URL" -c "
SELECT
    fr.id,
    fr.relation_type,
    fr.general_fact_id,
    LEFT(general.fact_text, 50) as general_fact,
    fr.specific_fact_id,
    LEFT(specific.fact_text, 50) as specific_fact,
    LEFT(fr.reason, 40) as reason
FROM fact_relations fr
JOIN facts general ON fr.general_fact_id = general.id
JOIN facts specific ON fr.specific_fact_id = specific.id
ORDER BY fr.created_at DESC
LIMIT 20;
"
