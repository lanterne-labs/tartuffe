# Fact Deduplication System

## Overview

The Digital Mirror system automatically deduplicates similar facts using semantic similarity (embeddings). This prevents duplicate facts from the same news story appearing multiple times.

## How It Works

### 1. Semantic Similarity Detection
- Every fact is converted to a vector embedding (using OpenAI's `text-embedding-3-small`)
- When inserting a new fact, the system searches for similar canonical facts
- Similarity threshold: **80%** (default, configurable via `FACT_SIMILARITY_THRESHOLD`)

### 2. Canonical vs Related Facts
- **Canonical Fact**: The first occurrence of a unique fact (used for contradiction detection)
- **Related Facts**: Similar variations linked to the canonical fact (preserved for traceability)

### 3. Example: Marine Le Pen Ineligibility News

If 10 news articles report "Marine Le Pen may be ineligible for presidency":

```
Canonical Fact (ID: 42):
"Marine Le Pen may be declared ineligible to run for president due to legal issues"
├── Related Fact (ID: 43): "Le Pen faces potential ineligibility for 2027 election"
├── Related Fact (ID: 44): "Court may bar Marine Le Pen from presidential candidacy"
├── Related Fact (ID: 45): "Marine Le Pen's eligibility questioned by prosecutors"
└── ... (7 more related facts)
```

### 4. Benefits
- **Contradiction detection**: Only compares against canonical facts (no duplicates)
- **Traceability**: All sources preserved - you can see all articles
- **Honesty score**: Not penalized multiple times for the same story
- **Storage**: Efficient - similar facts share embeddings

## Source Types

Facts are categorized by source:

| Source Type | Description | Example |
|------------|-------------|---------|
| `assemblee_law_page` | French National Assembly voting records | "voted FOR a budget law" |
| `news` | News articles from web search | "Marine Le Pen faces trial" |
| `NULL` | Legacy or uncategorized | (should be cleaned up) |

## Management Commands

### Check Current State
```bash
# Show facts by source type
./scripts/show_facts_by_source.sh

# Show canonical facts with related fact counts
./scripts/show_fact_groups.sh
```

### Delete News Facts (Keep Law Voting Data)
```bash
./scripts/delete_news_facts.sh
```

This deletes all `source_type='news'` facts while keeping law voting facts.

### Search News and Insert Facts
```bash
# Queue a news search
./digital-mirror news queue "Marine Le Pen" --location France --limit 20

# Process the queue
./digital-mirror news run

# Check results
./scripts/show_fact_groups.sh
```

## Configuration

### Adjust Similarity Threshold
```bash
# More strict (fewer groupings, more canonical facts)
export FACT_SIMILARITY_THRESHOLD=0.90

# More lenient (more aggressive grouping)
export FACT_SIMILARITY_THRESHOLD=0.70
```

Default: `0.80` (80% similarity)

## Database Schema

```sql
facts
├── id (primary key)
├── fact_text (normalized text)
├── embedding (vector for similarity search)
├── is_canonical (boolean)
└── canonical_fact_id (references facts.id, NULL for canonical facts)

fact_sources
├── id (primary key)
├── fact_id (references facts.id)
├── source_url (article URL)
├── source_type ('news', 'assemblee_law_page', etc.)
├── metadata (JSONB - story_token, law_key, etc.)
└── published_at (article publication date)

fact_mentions
├── fact_source_id (references fact_sources.id)
└── person_id (references persons.id)
```

## Troubleshooting

### Too Many Duplicate Facts
If you see many similar facts not being grouped:
1. Check `FACT_SIMILARITY_THRESHOLD` - lower it (e.g., 0.70)
2. Verify embeddings are being generated correctly
3. Check if facts mention the same person (grouping only happens within same person)

### Facts Not Being Grouped
If different facts are being grouped together incorrectly:
1. Raise `FACT_SIMILARITY_THRESHOLD` to 0.85 or 0.90
2. Check fact text quality - ensure they're normalized properly

### View Similarity Scores
```sql
-- Find facts similar to a specific fact
SELECT
    f.id,
    f.fact_text,
    1 - (f.embedding <=> target.embedding) as similarity
FROM facts f
CROSS JOIN (SELECT embedding FROM facts WHERE id = 42) target
WHERE f.id != 42
  AND f.is_canonical = true
ORDER BY similarity DESC
LIMIT 10;
```
