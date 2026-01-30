# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Digital Mirror is a fact-checking and honesty-scoring system for political figures. It extracts facts from web content and political data sources, stores them with semantic embeddings, and uses LLM-based contradiction detection to compute honesty scores.

**Tech Stack**: Go 1.23 backend (Chi router, pgx/PostgreSQL, pgvector), SvelteKit 5 frontend, Supabase for local development.

## Common Commands

### Database
```bash
make db-start          # Start Supabase (ports 55321-55327)
make db-stop           # Stop Supabase
```

**⚠️ CRITICAL: Never run `supabase db reset` without asking the user first!**
- `supabase db reset` drops ALL data including facts, persons, and processed laws
- This command is destructive and cannot be undone
- Only use for fresh starts or when explicitly requested

**Applying Migrations**:
```bash
# Preferred: Apply new migrations incrementally without data loss
# TODO: Implement goose migration tool (github.com/pressly/goose) for incremental migrations
# For now, new migrations are auto-applied by Supabase on restart
```

### Backend
```bash
go build -o dm .                                # Build binary
./dm web serve --port 8080                      # Run API server
./dm persons add "Name"                         # Add a tracked person
./dm facts list --person "Name"                 # List facts for a person
./dm facts delete --person "Name"               # Delete all facts for a person
```

### News Search
```bash
# Search news and scrape articles (uses SerpAPI + webcrawler)
./dm plugin news search --targets "Marine Le Pen" --max-parse 10

# Search news with caching (saves scraped content to disk)
./dm plugin news search --targets "Marine Le Pen" --cache-dir ./scraped_content/news_cache

# Re-extract facts from cached content only (no scraping, no API calls except LLM)
./dm plugin news search --targets "Marine Le Pen" --cache-dir ./scraped_content/news_cache --cache-only

# Ingest facts from local JSON files directly
./dm ingest files --dir ./scraped_content/news_cache --target "Marine Le Pen"
```

The `--cache-only` flag is useful for re-running fact extraction with updated prompts without re-scraping content. Cached content is stored in target-specific subdirectories (e.g., `marine_le_pen/`).

### Frontend
```bash
cd frontend
npm install
npm run dev -- --port 3000    # Dev server (expects API on 8080)
npm run build                 # Production build
npm run check                 # Type checking
```

### Testing
```bash
make test              # Run all Go tests (starts Supabase automatically)
make test-short        # Run tests without verbose output
```

Tests require Supabase running. The Makefile automatically sets `DATABASE_URL` from `supabase status`.

To run a single test:
```bash
export DATABASE_URL=$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//')
go test -v ./backend/storage -run TestSpecificName
```

## Architecture

### Data Flow Pipeline
1. **Discovery**: Plugin (e.g., frpolitics) downloads raw data from external sources
2. **Entity Creation**: Create person/party records with temporal affiliations
3. **Fact Generation**: Convert domain events (votes, statements) to structured Fact objects
4. **Embedding**: Generate vector embeddings via OpenAI-compatible API, store in pgvector
5. **Contradiction Detection**: Query K-nearest neighbors per person, compare with LLM
6. **Score Update**: Recalculate honesty score: `1 - (contradictions / max(1, facts))`
7. **Event Emission**: Insert events to `event_log` table for SSE streaming

### Key Components

- **`backend/storage/postgres.go`**: Core data layer - facts, persons, contradictions, affiliations, event logging. All DB operations go through here.

- **`backend/plugins/frpolitics/`**: French politics plugin - downloads vote data from Assemblée Nationale APIs, creates deputies/parties, generates facts from voting records. Entry point is `run.go`.

- **`backend/contradiction/`**: LLM-based contradiction detection. Uses semantic similarity (pgvector) to find candidate facts, then LLM to judge contradiction.

- **`cmd/web.go`**: HTTP API with Chi router. Endpoints for persons, facts, contradictions, and SSE stream (`/api/events`).

- **`frontend/src/lib/realtime.ts`**: SSE client singleton with exponential backoff reconnection.

### Database

PostgreSQL with pgvector extension. Migrations in `supabase/migrations/` are idempotent (safe to re-run).

**Migration Strategy**:
- New migrations are auto-detected and applied by Supabase on next `make db-start`
- Migrations should be incremental (ALTER TABLE, not DROP/CREATE)
- **Never reset the database without user permission** - data loss is permanent
- Consider implementing goose (github.com/pressly/goose) for better migration control

Key tables: `persons` (with `honesty_score`), `facts` (with `embedding` vector), `contradictions`, `person_links` (temporal affiliations), `event_log` (for SSE), `news_queue` (queue for news search).

## Environment Variables

Required in `.env`:
```
DATABASE_URL=postgresql://postgres:postgres@127.0.0.1:55322/postgres
OPEN_AI_KEY=sk-...
OPEN_AI_API=https://api.openai.com/v1
```

Optional:
```
OPEN_AI_CHAT_MODEL=gpt-4o-mini
OPEN_AI_EMBED_MODEL=text-embedding-3-small
GOOGLE_SEARCH_API_KEY=...
FIRECRAWL_API_KEY=...
```

## Development Status

**Nothing is in production yet.** Don't worry about backward compatibility - if something breaks, fix it. This is pre-production development.

## Conventions

- **Logging**: Use zerolog (`github.com/rs/zerolog/log`) with structured fields
  - **Never silence errors**: All errors must be logged, never swallowed silently
  - Use `log.Error()` for actual failures (red in console)
  - Use `log.Warn()` for recoverable issues or degraded behavior (yellow in console)
  - Use `log.Info()` for important operational events
  - Use `log.Debug()` for detailed troubleshooting info
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`
- **Database**: Use transactions for multi-step operations; connection pooling via pgxpool. **NEVER run `supabase db reset` without asking user first** - it destroys all data. Write incremental migrations instead.
- **API**: REST with JSON, pagination via `limit` and `offset` query params
- **Frontend**: TypeScript interfaces in `src/lib/types.ts`, API base in `src/lib/config.ts`
- **No workarounds**: Fix the root cause of issues rather than creating workarounds to bypass them
- **Searching**: Use `ack` for searching code - it's faster and smarter than grep, automatically ignores version control directories and binary files
