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
supabase db reset      # Apply all migrations from scratch
```

### Backend
```bash
go build -o digital-mirror .                    # Build binary
./digital-mirror web serve --port 8080          # Run API server
./digital-mirror cron run                       # Run fact ingestion pipeline
./digital-mirror persons add "Name"             # Add a tracked person
```

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

Key tables: `persons` (with `honesty_score`), `facts` (with `embedding` vector), `contradictions`, `person_links` (temporal affiliations), `event_log` (for SSE).

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

## Conventions

- **Logging**: Use zerolog (`github.com/rs/zerolog/log`) with structured fields
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`
- **Database**: Use transactions for multi-step operations; connection pooling via pgxpool
- **API**: REST with JSON, pagination via `limit` and `offset` query params
- **Frontend**: TypeScript interfaces in `src/lib/types.ts`, API base in `src/lib/config.ts`
