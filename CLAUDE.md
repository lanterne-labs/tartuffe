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

**`supabase db reset`**: You can run this when needed (e.g., to apply new migrations). It drops all data but nothing is in production yet. Note that another application also uses Supabase, but this should be fine.

**Applying Migrations**:
- Migrations are applied by **goose** at `dm web serve` startup (from `/migrations` in the container, `supabase/migrations/` locally).
- **Every new migration MUST start with `-- +goose Up` and include a `-- +goose Down` block.** Without these annotations goose fails to parse the file and the server panics at boot (`unexpected state 0 on line ...`) — the container crash-loops and the deploy is marked FAILED on the host. Copy the format from any existing migration (e.g. `20260510000000_resize_embedding_to_4096.sql`).
- Locally, `supabase db reset` re-applies everything from scratch and is fine to run.

### Backend
```bash
go build -o dm .                                # Build binary
./dm web serve --port 8080                      # Run API server
./dm persons add "Name"                         # Add a tracked person
./dm facts list --person "Name"                 # List facts for a person
./dm facts delete --person "Name"               # Delete all facts for a person
```

### Collection tasks and the worker
```bash
# Ask the plugins that read by period (news, twitter, viepublique) for the facts of a person over a period.
# The worker runs the tasks as soon as it is free. A period a plugin has fetched is never fetched again.
./dm collect add "Marine Le Pen" --from 2024-01-01 --to 2024-07-01   # --to defaults to today
./dm collect list --status pending
./dm collect cancel 12 13
./dm queue add "Marine Le Pen"                   # the watch list: one task per day for yesterday, per period plugin
./dm worker run                                  # the long-lived worker (the host service `worker`)
# WORKER_PAUSED=true on the worker service stops all its work (tasks, feed, maintenance) until removed
```

The worker (`backend/worker`) replaces the nightly cron. It runs the work in the order of priority. The daily feed after 03:00 UTC (frpolitics votes and transcripts, the backfills for 1 h, the watch list tasks) goes before every task: it runs beside the tasks as soon as it is due, and while it is due or runs no backfill task starts and no maintenance pass (only the watch list's tasks, added at the start of the feed, are claimed; the backfills already running go on). The watch list tasks (the day before) have priority 50, above the backfills (`--priority`, 0 to 20). Within a priority, a task of a person no task runs for goes first (the facts of one person are inserted one at a time). The maintenance pass (stance refresh, stance merge, the fold of the split claim groups of the persons whose facts changed, contradiction detection over the persons with new facts, duplicate repoint, regroup, audit every 5 days, translations) runs alone, without a task or the feed, since it rewrites the stances they insert into: when no task is pending after facts were added, and at the latest every 6 h although tasks are pending (the worker stops claiming, lets the running tasks end, runs the pass, then goes on: a due pass goes before a new task, so free lanes do not fill first; while a feed is due or runs, the pass waits for its end). The pass judges what changed since the start of the last one (`job_metrics`, job `maintenance`). Its stance merge makes at most 2000 LLM calls, the persons at once as the seats serve, its fold at most 2000 grouping gate calls (a pair the gate judged at insertion or in an earlier fold keeps its stored verdict, `occurrence_checks`), and its detection judges at most 2000 pairs (about 2 h); a pass cut there records `cut_since_unix`, and the next pass goes on over the same persons once the tasks and the feed left room.

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
export DATABASE_URL=$(supabase status --output env | grep '^DB_URL=' | sed -e 's/^DB_URL=//' -e 's/^"//' -e 's/"$//')
go test -v ./backend/storage -run TestSpecificName
```

## Architecture

### Data Flow Pipeline
1. **Discovery**: Plugin (e.g., frpolitics) downloads raw data from external sources
2. **Entity Creation**: Create person/party records with temporal affiliations
3. **Fact Generation**: Convert domain events (votes, statements) to structured Fact objects
4. **Embedding**: Generate vector embeddings via OpenAI-compatible API, store in pgvector
5. **Contradiction Detection**: Query K-nearest neighbors per person, compare with LLM
6. **Score Update**: Recalculate honesty score: `exp(-4 * contradictions/facts - 0.02 * contradictions)` (hybrid: ratio decay k=4 + absolute decay j=0.02)
7. **Event Emission**: Insert events to `event_log` table for SSE streaming

### Key Components

- **`backend/storage/postgres.go`**: Core data layer - facts, persons, contradictions, affiliations, event logging. All DB operations go through here.

- **`backend/plugins/frpolitics/`**: French politics plugin - downloads vote data from Assemblée Nationale APIs, creates deputies/parties, generates facts from voting records. Entry point is `run.go`. A ballot on an article of a bill carries what the article does (`articles.go`): the text of the séance comes from the amendments voted in it, the article from the PDF of the text (`pdftotext`, poppler-utils in the image), the statement from the LLM, cached in `vote_articles`. `dm plugin frpolitics --mode rearticle [--rejudge]` rewrites the ballots stored before. A local votes run needs `pdftotext` on the PATH (poppler).

- **`backend/plugins/viepublique/`**: the speeches, interviews, press conferences and communiqués of the executive on vie-publique.fr (DILA open data, Etalab licence v2: the facts carry the attribution). The daily index (`vp_discours.json`, about 240 MB, cached 12 h in `VIEPUBLIQUE_CACHE_DIR`, else the temp dir) names the speakers and dates; the page holds the text. A task over a period takes the speeches of the person given or put online in it (most go online days later), never those where the person is listed as a journalist. Only the person's own turns are extracted, each after the question it answers. One document per speech and person; a window is logged (`viepublique_fetch_log`) once all its pages are stored, so a site outage leaves it open.

- **`backend/webfetch/`**: the way out to the sites we read. Each site owns how it is fetched: a `webfetch.Site` gets the shared `Client` and the `Request` (URL, source, purpose, person, period) and does what the site needs: a login, a cookie step, its own pace (`webfetch.Pace`: a rate, how many at once, halved on a 429 or 503 and climbing back), its own retries. A plugin registers the sites it knows (`webfetch.Register`, from `init`: vie-publique's bot-check redirect is in `plugins/viepublique/site.go`); any other host goes to the default site, a plain GET at one request a second and two at once per domain, with another browser tried on a 403. `Client.Do` adds the common headers of a desktop Chrome in French (never the name of the app) and records every request in `fetch_log` (source, domain, status, outcome, duration); a page outcome that is not a status (article ok, failed, too thin) is a `Note`.

- **`backend/contradiction/`**: LLM-based contradiction detection. Uses semantic similarity (pgvector) to find candidate facts, then LLM to judge contradiction.

- **`cmd/web.go`**: HTTP API with Chi router. Endpoints for persons, facts, contradictions, and SSE stream (`/api/events`).

- **`frontend/src/lib/realtime.ts`**: SSE client singleton with exponential backoff reconnection.

### Database

PostgreSQL with pgvector extension. Migrations in `supabase/migrations/` are idempotent (safe to re-run).

**Migration Strategy**:
- Migrations are run by **goose** on `dm web serve` startup (in prod) and applied locally by `supabase db reset` / `make db-start`.
- **Every new migration MUST begin with `-- +goose Up` and include a `-- +goose Down` block** — without them goose throws a parse error at boot and the deploy fails.
- Migrations should be incremental (ALTER TABLE, not DROP/CREATE).
- **Never reset the database without user permission** - data loss is permanent.

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
OPEN_AI_CHAT_MODEL=mirror-llm      # stable name; see "Local LLM"
OPEN_AI_EMBED_MODEL=text-embedding-3-small
GOOGLE_SEARCH_API_KEY=...
FIRECRAWL_API_KEY=...
FRPOLITICS_CACHE_DIR=/data/frpolitics-cache  # Persistent cache for AN data (the host volume mount)

# LLM routing (backend/llm/endpoint.go)
LLM_PROFILE=local                # local (default) or claude-team; the profile routes each LLM task to an endpoint.
                                 # local: the GPU box (OPEN_AI_API) with the OpenRouter fallback, as before.
                                 # claude-team: the Team seats first; when every seat is at its usage limit, the
                                 # Claude API (ANTHROPIC_API_KEY) serves, capped per UTC day.
CLAUDE_API_DAY_CAP_USD=25        # the cap of the API key per UTC day (default 25): the seats have no cap of ours,
                                 # only Anthropic's usage windows; the key stops at the cap and serves again the next day
CLAUDE_TEAM_PLAN_TOKENS=a=...,b=...  # seat tokens of the Team plan, as name=token pairs, in the order of use:
                                 # a seat at its usage limit gives way to the next; the claude CLI must be on the PATH
CLAUDE_TEAM_PLAN_SEAT=a          # optional: the seat to use first (the order then wraps around)
ANTHROPIC_API_KEY=sk-ant-...     # Claude API key: the overflow of the seats on claude-team (optional: without it the
                                 # worker waits for the seat reset), and the goldens (make llm-goldens LLM_CLAUDE=...)

# Search providers — assembled into a chain (tried in this order; whichever key is set is added).
# Priority: Serper > Brave > SerpAPI. Each is optional; at least one must be set.
SERPER_API_KEY=...               # https://serper.dev — supports tbs date-range filter
BRAVE_SEARCH_API_KEY=...         # https://brave.com/search/api
SERPAPI_KEY=...                  # https://serpapi.com
```

## Local LLM

Production and local runs call one model name, `mirror-llm`, at `OPEN_AI_API` (production: the Cloudflare tunnel to the local GPU box). llama-swap on that box maps the name to the real model (`~/claude-ai/llama-swap.yaml`); today it is Qwen3.8-27B (unsloth UD-Q3_K_XL GGUF, llama.cpp, fully in the 16 GB of VRAM). `FALLBACK_CHAT_MODEL` names the same model on OpenRouter.

- Every LLM decision call declares its task (`ChatRequest.Task`, `backend/llm/task.go`). One table there says which tasks think; the client then turns thinking on with a thinking budget (`llm.ThinkingBudgetTokens`) on top of the call's own `MaxTokens`. Thinking is off on the server by default. Today only the grouping gate (`TaskOccurrence`) thinks: a task thinks only when its golden shows a gain, and the nightly must still fit its window (a thinking call costs about 3 times a plain call).
- To measure a task with thinking: `make llm-goldens LLM_THINK=occurrence,detect` (the exact set of thinking tasks for the run, or `none`).
- To change the model:
  1. Serve the candidate on a free port (for example `llama-server ... --port 8095`).
  2. Measure it: `make llm-goldens LLM_API=http://127.0.0.1:8095/v1 LLM_MODEL=<name>`, then `make llm-eval` with the same values (grouping, about 2 h). Compare with the reference numbers in `backend/core/occurrence_eval_test.go`. Measure the thinking table again too: a new model can gain on other tasks.
  3. If it is better, edit the `mirror-llm` entry in `llama-swap.yaml` (llama-swap reloads the file). Nothing changes on the host, except `FALLBACK_CHAT_MODEL` if the OpenRouter name changes.
  4. Record the result in CHANGES.md.

## Deployment (the host)

Services on the host:

- **`mirror`** — long-lived `dm web serve` (built from root `Dockerfile`). Hosts the HTTP API, the balance alerter, and exposes Prometheus metrics on `METRICS_PORT` (default 9090).
- **`worker`** — long-lived `dm worker run` (root `Dockerfile`, start command set in the service settings). Claims the collection tasks (`dm collect add`) and runs the ingest of their plugin for their person and period, as many tasks at once as the LLM endpoint serves (`llm.Concurrency()`: one on the local GPU box, on claude-team the processes of every seat: `CLAUDE_TEAM_PLAN_PROCS_PER_SEAT`, default 4, times the seats; every seat that is not at its limit serves at the same time); the facts of one person are inserted one at a time across the tasks. The daily feed and the maintenance pass in the order of priority (see Collection tasks above). Serves Prometheus metrics on `METRICS_PORT` (default 9090). Same env vars as `mirror` except no HTTP `PORT`. On a deploy the tasks it is on go back to pending and resumes where its fetch log stopped.
- **`observability`** — colocated Prometheus + Grafana under s6-overlay (built from `deploy/observability/Dockerfile`). Scrapes `mirror.host.internal:9090/metrics` and `worker.host.internal:9090/metrics` every 15s. A push does not deploy it: deploy it with `host up deploy/observability --path-as-root --service observability`. Grafana exposed publicly via the host-issued domain. One persistent volume mounted at `/data` (Prometheus TSDB at `/data/prometheus`, Grafana state at `/data/grafana`). Grafana alerts (`grafana/provisioning/alerting/worker.yml`: feed missing after 06:00 UTC, no maintenance pass ended for 12 h) go to Telegram: the service needs `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` before it is deployed.

To collect facts, add tasks with `./dm collect add`; the worker runs them.

### Quality tracking
The Grafana dashboard **Quality** (`deploy/observability/grafana/dashboards/quality.json`, from the application DB) draws:
- the precision of the detection, from the verdicts in `contradiction_reviews`. To review, run `dm contradictions review sample --reviewer <name> --max 30 > todo.jsonl`, add `"verdict"` (real, debatable, false) and an optional `"note"` to each line, then run `dm contradictions review record --reviewer <name> < todo.jsonl`. A review keeps the facts as they were reviewed: it survives a reset or an audit, and it counts in the week the contradiction was detected;
- the quality gauges: stances, one-fact stances, claim groups, contradictions per score bundle. They are rows of `job_metrics` under the job `quality`, recorded at the end of each maintenance pass, or now with `dm quality record`;
- the verdicts of the periodic LLM audit.

### Sources
The Grafana dashboard **Sources** (`sources.json`) shows, per source (news, viepublique, assemblee, photos, archive) and for the news per site, what the sites answered (ok, throttled, refused, gone, errors, timeouts), what the pages gave (articles ok, failed, empty or too thin, the bot check), the documents and facts per source, and the failures per hour. It reads `fetch_log` (90 days kept) and `source_documents`.

### Tasks
The Grafana dashboard **Tasks** (`tasks.json`) lists the collection tasks: the ones running, then the pending ones in the order the worker takes them (highest priority, then oldest; a task put back waits for its `not_before`), the pending tasks per operation and plugin, and the tasks that ended recently with their facts and errors.

The admin password of Grafana is the service variable `GF_SECURITY_ADMIN_PASSWORD`: the start script resets it at every start (`s6-rc.d/grafana/run`).

### Operations tracking
The Grafana dashboard **Operations** (`operations.json`) shows where each long operation stands: how far it is, its speed over the last 3 hours, when it should end at that speed, and what it produces (pairs judged, contradictions, claim groups, facts).
- The fold (`dm facts fold --apply` over everyone) and the detection over every person record their progress in persons, as `job_metrics` rows under the job `progress` (`storage.RecordProgress`). A new long operation without a table of its own records its progress the same way.
- A collection batch is named at its creation: `dm collect add "Name" --from 2024-10-01 --operation "backfill 2y"`. Its progress is its tasks (`collection_tasks.operation`).

## Development Status

**Nothing is in production yet.** Don't worry about backward compatibility - if something breaks, fix it. This is pre-production development.

## Conventions

- **LLM output order: reasoning first, verdict last.** Every prompt that asks the model for a decision (same/different, keep/drop, yes/no, a label) must make the model write its reasoning BEFORE the decision field, and the decision must be the LAST field of the reply (JSON key order or line order). A model that writes the verdict first decides without reasoning and then justifies it. Parsers read fields by name, so the order costs nothing in code.
- **Every request to a site we read goes through `webfetch`** (`webfetch.Fetch`, or `webfetch.Shared().Do` for a stream): no `http.Client` of a plugin's own for a site, no User-Agent of our own. What a site needs (a login, a pace) is its `webfetch.Site`, in its plugin. The APIs we call (LLM, search, Twitter, Wikipedia, which asks robots to name themselves) are not sites and keep their clients.
- **How much LLM work runs at once is the endpoint's call.** Work made of LLM calls (facts, pairs, persons) goes through `llm.ForEach`: as many items at once as the active endpoint serves (`llm.Concurrency()`: 1 on the local GPU box, the seats' process count on claude-team). No `--parallel` flag, no constant, no hand-made goroutine pool for LLM work.
- **Logging**: Use zerolog (`github.com/rs/zerolog/log`) with structured fields
  - **Never silence errors**: All errors must be logged, never swallowed silently
  - Use `log.Error()` for actual failures (red in console)
  - Use `log.Warn()` for recoverable issues or degraded behavior (yellow in console)
  - Use `log.Info()` for important operational events
  - Use `log.Debug()` for detailed troubleshooting info
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`
- **Database**: Use transactions for multi-step operations; connection pooling via pgxpool. `supabase db reset` is fine to run when needed (e.g., applying migrations). Prefer incremental migrations when possible.
- **API**: REST with JSON, pagination via `limit` and `offset` query params
- **Frontend**: TypeScript interfaces in `src/lib/types.ts`, API base in `src/lib/config.ts`
- **No workarounds**: Fix the root cause of issues rather than creating workarounds to bypass them
- **Searching**: Use `ack` for searching code - it's faster and smarter than grep, automatically ignores version control directories and binary files

## Headless browser

- Never start Chromium (or any browser) with `--enable-gpu` or `--ignore-gpu-blocklist`. On this machine the AMD iGPU drives the display. On 2026-10-03 and 2026-10-04, a headless Chromium with these flags caused an iGPU page fault. The GPU reset then stopped the compositor and closed the desktop session of the owner.
- Use software rendering: `--use-angle=swiftshader --enable-unsafe-swiftshader`. WebGL and canvas work on the CPU with these flags.
- Stop the headless browser when the check ends. Do not leave it running in the background.


<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->
