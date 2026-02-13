#!/bin/bash
# Delete all news-based facts while keeping law voting data

set -e

# Get database URL from environment or supabase status
if [ -z "$DATABASE_URL" ]; then
    echo "DATABASE_URL not set, getting from supabase status..."
    export DATABASE_URL=$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//')
fi

if [ -z "$DATABASE_URL" ]; then
    echo "Error: Could not determine DATABASE_URL"
    exit 1
fi

echo "Connected to: $DATABASE_URL"
echo ""
echo "This will DELETE all news-based facts (source_type='news')"
echo "Law voting facts (source_type='assemblee_law_page') will be kept."
echo ""

# Show current counts
echo "=== Current database state ==="
psql "$DATABASE_URL" -c "
SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
GROUP BY fs.source_type
ORDER BY fact_count DESC;
"

echo ""
echo "=== Facts that will be deleted ==="
psql "$DATABASE_URL" -c "
SELECT
    COUNT(DISTINCT f.id) as facts_to_delete,
    COUNT(DISTINCT fs.id) as sources_to_delete,
    COUNT(DISTINCT fm.id) as mentions_to_delete
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
LEFT JOIN fact_mentions fm ON fs.id = fm.fact_source_id
WHERE fs.source_type = 'news'
   OR fs.source_type IS NULL
   OR (fs.metadata IS NULL OR NOT (fs.metadata ? 'law_key'));
"

echo ""
read -p "Continue with deletion? (yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo "Aborted."
    exit 0
fi

echo ""
echo "=== Deleting news facts ==="
psql "$DATABASE_URL" -c "
DELETE FROM facts
WHERE id IN (
    SELECT DISTINCT f.id
    FROM facts f
    JOIN fact_sources fs ON f.id = fs.fact_id
    WHERE fs.source_type = 'news'
       OR fs.source_type IS NULL
       OR (fs.metadata IS NULL OR NOT (fs.metadata ? 'law_key'))
);
"

echo ""
echo "=== After deletion ==="
psql "$DATABASE_URL" -c "
SELECT
    COALESCE(fs.source_type, 'NULL') as source_type,
    COUNT(DISTINCT f.id) as fact_count,
    COUNT(DISTINCT fs.id) as source_count
FROM facts f
JOIN fact_sources fs ON f.id = fs.fact_id
GROUP BY fs.source_type
ORDER BY fact_count DESC;
"

echo ""
echo "Done! News facts have been deleted."
