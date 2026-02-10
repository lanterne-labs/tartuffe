## Digital Mirror – Backend & Frontend Guide

This document explains everything you need to run, develop, and extend the Digital Mirror stack in one place.

---

### 1. Prerequisites

- **Go 1.23+**
- **Node 20+** (or the version required by SvelteKit)
- **Supabase CLI** (for local Postgres + realtime infrastructure)
- `supabase start` consumes ports **55321–55327**; ensure they are free.

Environment variables live in `.env`. Key ones:

```
DATABASE_URL=postgresql://postgres:postgres@127.0.0.1:55322/postgres
OPEN_AI_KEY=...
OPEN_AI_API=...
```

---

### 2. Database & Migrations

All SQL lives under `supabase/migrations/`.

- `20260109120000_add_honesty_score_to_persons.sql`
  - Adds `persons.honesty_score` with default `1.0`.
  - Uses `DO $$ ... EXCEPTION WHEN duplicate_object THEN NULL; END $$` for idempotent constraint creation.
- `20260109121000_add_event_log_table.sql`
  - Creates `event_log` table (id, timestamps, topic, entity metadata, JSON payload).

Workflow:

```bash
# start Supabase (Postgres, Studio, etc.)
make db-start

# apply all migrations from scratch
supabase db reset
```

---

### 3. Backend (Go)

#### 3.1 Commands

| Command | Description |
|---------|-------------|
| `./digital-mirror cron run` | Core fact ingest + contradiction detection (needs API keys). |
| `./digital-mirror persons add "Name"` | Adds a tracked person. |
| `./digital-mirror web serve --port 8080` | Runs REST API + SSE gateway described below. |

Build the binary:

```bash
go build -o digital-mirror .
```

#### 3.2 Storage layer updates (`backend/storage/postgres.go`)

- `RecordContradiction` now:
  - Detects **new insert vs update** via `RETURNING id, (xmax = 0) AS was_inserted`.
  - On a new insert:
    - Recomputes honesty score using: `1 - (contradictions / max(1, facts))`, clamped to `[0, 1]`.
    - Updates `persons.honesty_score`.
    - Inserts `person_updated` and `contradiction_added` events into `event_log`.
- Helper methods:
  - `getPersonFactsCount`, `getPersonContradictionsCount`, `updatePersonHonestyScore`, `insertEvent`.
  - Query helpers for the API:
    - `GetPersonsRankedByScore`
    - `GetPersonByID`
    - `GetPersonFacts`
    - `GetPersonContradictions`

#### 3.3 HTTP API (`cmd/web.go`)

Uses Chi router + middleware (`logger`, `recoverer`, `timeout`, **CORS**). Routes:

```
GET /api/persons?limit&offset
GET /api/person/:id
GET /api/person/:id/facts?limit&offset
GET /api/person/:id/contradictions?limit&offset
GET /api/events?since_id   (SSE stream)
```

**CORS:** `github.com/go-chi/cors` allows `http://localhost:3000`, `127.0.0.1:3000`, and `*` for dev.

**SSE strategy:** server polls `event_log` every 250 ms while holding the HTTP stream. Clients send `since_id` to resume after reconnects.

Run:

```bash
./digital-mirror web serve --port 8080
```

---

### 4. Honesty Score Flow

1. `cron run` (or the facts listener) stores a fact.
2. When the contradiction detector finds a new contradiction:
   - Calls `RecordContradiction`.
   - Score recalculates inside the same DB transaction.
   - Event(s) go to `event_log`.
3. SSE clients receive `person_updated` or `contradiction_added`, then refetch affected data.

This ensures:
- Scores are always persisted in Postgres.
- Realtime updates are durable (replayable) thanks to `event_log`.

---

### 5. Frontend (SvelteKit)

Location: `frontend/`

#### 5.1 Install & run

```bash
cd frontend
npm install
npm run dev -- --port 3000
```

The Vite dev server proxies requests to the API by calling absolute URLs (`http://localhost:8080` by default).

#### 5.2 Configuration (`src/lib/config.ts`)

`API_BASE` resolves in this order:
1. `import.meta.env.VITE_API_BASE` (if set).
2. Otherwise, at runtime it mirrors the current origin.
   - Special case: when running on port 3000, it assumes Go API is on port 8080.

Set a custom base by creating `.env.local` in `frontend/`:

```
VITE_API_BASE=https://api.example.com
```

#### 5.3 Pages / components

- `/` (`src/routes/+page.svelte`)
  - Fetches `/api/persons`.
  - Shows honesty bar, party badge, pagination (`Load More`).
  - Subscribes to SSE events via `src/lib/realtime.ts` and refreshes when any person changes.
- `/person/[id]`
  - Shows person header, honesty meter.
  - Tabs for `Facts` and `Contradictions` with pagination.
  - Re-subscribes to SSE events scoped to that person ID (refreshes current tab).

#### 5.4 Realtime client (`src/lib/realtime.ts`)

- Singleton `RealtimeManager` opens an `EventSource` to `/api/events`.
- Handles:
  - Last event ID tracking (`since_id`).
  - Exponential backoff reconnects (up to 30s).
  - Publishing events to a Svelte `writable` store (`realtimeEvents`).
- `subscribeToPersonEvents(personId, cb)` helper
  - `personId = -1` means “listen to all persons” (used on `/`).
  - Individual person pages pass their numeric ID.

---

### 6. Typical Local Workflow

```bash
# 1) Start DB stack (first terminal)
make db-start            # or supabase start

# 2) Reset DB if needed (applies migrations, seeds)
supabase db reset

# 3) Build backend binary (once per change)
go build -o digital-mirror .

# 4) Run API/SSE server (second terminal)
./digital-mirror web serve --port 8080

# 5) Run frontend dev server (third terminal)
cd frontend && npm run dev -- --port 3000

# 6) (Optional) seed people/facts
./digital-mirror persons add "Emmanuel Macron"
./digital-mirror cron run   # executes full fact pipeline
```

Visit:
- `http://localhost:3000` (frontend)
- `http://localhost:8080/api/persons` (API check)

---

### 7. Deployment Notes

- **Backend**: can be deployed to the host/Fly/etc. Just ensure `DATABASE_URL` points to the hosted Postgres. SSE endpoint is stateless (besides DB access).
- **Frontend**: build with `npm run build` and host via Vite adapters or static hosting. Set `VITE_API_BASE=https://your-api-domain`.
- **Supabase Alternatives**: because the Go server is the only client of Postgres, you can migrate to any Postgres provider (Neon, RDS, etc.) by updating `DATABASE_URL`.

---

### 8. Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|--------------|-----|
| `column "honesty_score" does not exist` | `supabase db reset` hasn’t been run since adding migration | Run `supabase db reset`. |
| Frontend “NetworkError” | API not reachable or CORS missing | Ensure Go server running + CORS middleware enabled. |
| SSE not updating | Client lost connection | Check browser console; SSE auto-reconnects, but API must be reachable. |
| No persons displayed | DB empty | Add via CLI (`./digital-mirror persons add "Name"`). |

---

This document should be kept up to date whenever backend or frontend behavior changes, so the entire stack remains understandable without hunting through multiple READMEs.

