# CHANGES.md

This file tracks high-level changes to the codebase, lessons learned, and mistakes to avoid.

## 2026-02-11: Job Metrics Table

### What was done
- **Migration**: New `job_metrics` table (job_type, target, metric, value, created_at) with indexes on job_type, created_at, metric. Designed for Grafana to query directly.
- **Storage**: `JobMetric` struct and `RecordJobMetrics` batch insert method on `PostgresStorer`.
- **CLI instrumentation**: All 6 CLI commands record per-run business metrics to `job_metrics` before returning: `news-search`, `twitter-fetch`, `contradictions-detect`, `ingest-files`, `photos-search`, `queue-process`. Metrics are skipped in dry-run mode (except queue-process which has no global dry-run).

### Key design decisions
- Batch insert (single query with N value tuples) to minimize round trips.
- Per-target metrics for news and twitter; aggregate metrics for contradictions, photos, and queue.
- No dry-run metrics recording to avoid polluting data.

## 2026-02-11: Frontend Dark Theme Redesign

### What was done
- **Global CSS design tokens**: Created `frontend/src/app.css` with CSS custom properties for dark navy theme (colors, glass-morphism, typography, shadows, transitions). Includes shimmer skeleton animation and global reset.
- **app.html**: Added SEO meta tags, Open Graph/Twitter cards, Inter font from Google Fonts.
- **Layout**: Sticky glass-morphism nav bar, page transitions using `svelte/transition` fly. Imports global CSS.
- **Dashboard (+page.svelte)**: Rewritten with Svelte 5 syntax (`$state`, `onclick`/`oninput`), grid layout with glass cards, SVG score rings (stroke-dasharray), shimmer skeleton loaders, pill filter buttons.
- **Person detail page**: Rewritten with Svelte 5 syntax (`$state`, `$derived`, `onclick`), hero section with SVG gauge, glass cards for facts/contradictions, dark-themed contradiction comparison panels, blue glow for highlighted facts, responsive layout.
- **Error page**: New `+error.svelte` with dark theme styling.
- **Realtime cleanup**: Removed `console.log` for routine SSE events (connection opened, reconnect attempts). Kept `console.error` for actual failures.

### Key design decisions
- All colors/spacing/shadows as CSS custom properties for consistency and easy theming.
- Glass-morphism via `backdrop-filter: blur(12px)` with semi-transparent backgrounds.
- SVG ring gauges use `stroke-dasharray` with circumference `2*PI*14` for score visualization.
- Svelte 5 runes (`$state`, `$derived`) replace legacy reactive declarations (`$:`, `let x = ...`).
- `console.error` calls in `loadMembers` and `loadFactPersons` converted to set `error` state variable instead.

## 2026-02-11: Backend Security Hardening

### What was done
- **Separate Metrics Port**: Moved `/metrics` endpoint from the main API router to a dedicated internal-only HTTP server on `METRICS_PORT` (default `9090`). `/health` is available on both servers. Prometheus/Grafana scrape `localhost:9090/metrics` without exposing metrics publicly.
- **HTTP Rate Limiting**: Added `github.com/go-chi/httprate` middleware. Limits requests per IP to `RATE_LIMIT_PER_MINUTE` (default 100). Returns 429 when exceeded.
- **Security Headers Middleware**: New `securityHeaders` middleware sets `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy`, `Content-Security-Policy`, and conditional `Strict-Transport-Security` (only when `X-Forwarded-Proto: https`).
- **Middleware Order**: Reordered to: securityHeaders, Logger, Recoverer, Timeout, RateLimit, CORS, metrics.

### Key design decisions
- Metrics server runs as a goroutine within the same process — no separate binary needed. If it fails, it logs an error but doesn't crash the main server.
- HSTS is conditional on `X-Forwarded-Proto` to avoid breaking local HTTP development.
- Rate limiting is applied after timeout but before CORS, so preflight OPTIONS requests also count toward the limit.

## 2026-02-10: Photo Search Plugin

### What was done
- **Migration**: Added `image_url TEXT` column to `persons` table.
- **SerpAPI Image Search**: Extended `backend/search/serpapi.go` with `SearchImages()` method using `google_images` engine. Returns `ImageResult` with dimensions, source, thumbnail.
- **Object Storage**: New `backend/objectstore/r2.go` package — S3-compatible client with `ObjectStore` interface, `Upload()` and `Delete()` methods via AWS SDK v2. Env vars: `STORAGE_ENDPOINT`, `STORAGE_ACCESS_KEY`, `STORAGE_SECRET_KEY`, `STORAGE_BUCKET`. No separate public URL — uses endpoint directly.
- **Storage Layer**: Added `ImageURL` to `PersonSummary`, updated all queries (GetPersonsRankedByScore, GetPersonByID, GetFactPersons, members query). New methods: `SetPersonImageURL()`, `GetPersonsWithoutImage()`.
- **Photos Plugin**: `backend/plugins/photos/` — searches images, scores candidates by resolution/aspect ratio/domain reputation/position, downloads best match, uploads to R2, stores URL. Scoring heuristic: max 100 pts (resolution 40 + aspect 20 + domain 25 + position 15). Tries up to 5 fallback candidates.
- **CLI**: `dm plugin photos search --targets "Name" [--all] [--force] [--dry-run]`.
- **Frontend**: Circular avatars on main page (48px), detail page (96px), and member list (24px). Falls back to first-letter placeholder.

### Key design decisions
- SerpAPI over Google CSE: same key, returns image dimensions for pre-download scoring.
- Download + re-host on R2: external URLs rot; R2 ensures permanent CDN-backed delivery.
- Deterministic scoring heuristic (no LLM cost) with domain reputation list for trusted sources (Wikipedia, government sites, major news).
- `ImageSearcher` and `Storer` interfaces on the Provider for testability.

## 2026-02-07: Generalize News Queue to Core Search Queue

### What was done
- **Migration**: Renamed `news_queue` table → `search_queue` (with index renames).
- **Storage**: Renamed 4 methods: `AddToNewsQueue` → `AddToSearchQueue`, `RemoveFromNewsQueue` → `RemoveFromSearchQueue`, `GetNewsQueue` → `GetSearchQueue`, `IsInNewsQueue` → `IsInSearchQueue`.
- **Moved `ResolveTargetName` to core**: Created `backend/core/resolve.go` with `PersonResolver` interface, `ResolvedTarget` type, and generic resolve functions. Deleted `backend/plugins/news/resolve.go`. All callers updated.
- **Top-level queue CLI**: New `cmd/queue.go` with `dm queue {add,remove,list,process}`. Queue is no longer under `dm plugin news queue` — it's a core concept. Process command orchestrates multiple plugins via `--plugins news,twitter`.
- **Removed `--queue` flag** from `dm plugin twitter fetch` (queue processing now centralized).
- **Twitter QPS rate limiting**: Added `qpsDelay` to `TwitterAPIIOFetcher` (default 5s for free tier). Configurable via `TWITTER_API_IO_QPS_DELAY_MS` env var.
- **LLM retry resilience**: Made 5xx errors and timeouts retryable in `backend/llm/client.go` (were `backoff.Permanent`). Only 4xx errors (except 429) are permanent. Added `ProviderConfig` struct for OpenRouter provider routing.
- **LLM metrics**: Instrumented `llm/client.go` with `metrics.IncrementLLMRetry()` for each retry reason and `metrics.ObserveLLMRequest()` for final outcome tracking.

### Key design decisions
- `PersonResolver` interface avoids import cycle between `core` and `storage`. Storage satisfies it via `GetAllPersonRecords()` adapter + existing `FindPersonsByFuzzyName()`.
- `SearchTarget` (news-specific) stays in news package. Callers convert from `core.ResolvedTarget` to `news.SearchTarget` where needed.
- Queue process runs plugins sequentially per person (news first, then twitter). Twitter is skipped if no handles exist for a person.
- Plugin dedup is owned by each plugin independently — no queue-level `--skip-recent` filtering. News uses `IsURLVisitable` + `news_search_log`; Twitter uses `twitter_fetch_log` date ranges + `IsURLVisitable`.

## 2026-02-06: Twitter/X Plugin (Phase 5)

### What was done
- **New plugin**: `backend/plugins/twitter/` — fetches tweets via twitterapi.io, extracts facts using existing LLM pipeline.
- **Migration**: `twitter_handles` table links persons to @handles; `twitter_fetch_log` tracks fetched date ranges for API-level dedup.
- **Storage**: 7 new methods on `PostgresStorer` for handle CRUD and fetch log.
- **CLI**: `dm plugin twitter {add,remove,list,fetch}` with `--force`, `--window-days`, `--dry-run`, date ranges.
- **Two-level dedup**: (1) `twitter_fetch_log` skips already-fetched date ranges, (2) `IsURLVisitable()` skips tweets already processed by LLM. Both bypassed with `--force`.
- **Metrics**: `tweets_processed_total` counter with status labels.

### Key design decisions
- `TweetFetcher` interface allows swapping the API provider later without touching the provider logic.
- Tweets are batched (default 20) into a single LLM call with `PluginInstructions` for twitter-specific extraction.
- Incremental fetching: without explicit dates, uses `last_fetched_at` → now.
- `twitter_fetch_log` uses DATE granularity (not timestamp) — simple but means partial-day fetches mark the whole day as done.
- `ResolveTargetName` from the news plugin is reused for the `add` command. Could be moved to a shared location later.

## 2026-02-04: Story Diversity — `--max-per-story` + Re-query with Shifted Windows

### What was done
- **`backend/plugins/news/provider.go`**: Added `MaxPerStory` field to `Options` (0 = no cap). Added `SkippedStoryCap`, `StoryCappedDates`, `StoryCappedTokens` fields to `Result`. Story cap logic in Phase 1 article selection: tracks `storyTokenCounts` per story_token, skips articles exceeding the cap, records skipped dates for re-query window calculation. Articles with empty `story_token` are never capped. Extracted `dateWindow` struct to package level. Added `computeReQueryWindow()` that finds the largest date gap outside the dominant story's cluster (with 1-day buffer). Added re-query logic in `CollectFactsWithDateWindows`: after each window, if story cap was hit and parse budget remains, computes a shifted sub-window and issues a second search (max 1 re-query per original window). Added `aggregateResult()` and `containsString()` helpers.
- **`cmd/news.go`**: Added `--max-per-story` flag, wired into `opts.MaxPerStory`.
- **`cmd/news_queue.go`**: Same pattern — added `--max-per-story` flag, wired into `opts.MaxPerStory`.
- **`backend/plugins/news/provider_test.go`**: Unit tests for `computeReQueryWindow` (cluster-at-start, cluster-at-end, cluster-in-middle, cluster-spans-all, empty dates) and `containsString`.

### Verification
- `go build` and `go vet ./...` both pass with 0 errors.
- All unit tests pass.

### Key design decisions
- Story cap happens at article selection (Phase 1), before expensive content extraction.
- Re-query only happens in windowed mode (`CollectFactsWithDateWindows`). `CollectFacts` just caps and reports.
- Max 1 re-query per window to bound API cost (at most 2 SerpAPI calls per window).
- URL dedup (`IsURLVisitable`) prevents re-query from double-processing same URLs.

### Lessons learned
- Google News `story_token` groups related articles about the same event. When a big story dominates, all 10-20 results share the same token, drowning out other topics.
- Re-querying with a shifted date window (avoiding the dominant story's cluster) effectively surfaces diverse stories that would otherwise be missed.

## 2026-02-03: Historical Data Scraping — Date Windowing (Phase 4)

### What was done
- **`backend/plugins/news/provider.go`**: Added `WindowDays` field to `Options` struct. Added `CollectFactsWithDateWindows()` method that breaks a `DateFrom→DateTo` range into windows of N days, calls `CollectFacts` per window, and aggregates results. Individual window errors are logged and collected but don't stop processing. Context cancellation returns partial results.
- **`cmd/news.go`**: Added `--window-days` flag. Validates that `--since` is set when `--window-days > 0`. Conditionally calls `CollectFactsWithDateWindows` instead of `CollectFacts` in the per-target loop.
- **`cmd/news_queue.go`**: Same pattern — added `--window-days` flag with validation that `--since` or `--days` is set, conditional call in the processing loop.

### Verification
- `go build` and `go vet ./...` both pass with 0 errors.

### Lessons learned
- Google News caps results at ~10-20 per query. Breaking large date ranges into smaller windows (e.g., 30-day windows) yields more comprehensive coverage.
- Setting `WindowDays = 0` in the per-window opts copy prevents recursion when `CollectFactsWithDateWindows` calls `CollectFacts`.

## 2026-02-02: Fix SSE (Phase 3)

### What was done
- **SSE backend fix** (`cmd/web.go`): Fresh SSE connections now start from `MAX(id)` in `event_log` instead of 0. Only reconnecting clients (those sending `since_id` query param) replay historical events. This prevents the flood of old events that was causing excessive API calls on the frontend.
- **Frontend SSE re-enabled**: Uncommented the `realtimeManager.connect()` / `subscribeToPersonEvents()` / `disconnect()` / `clearOldEvents()` calls in both `frontend/src/routes/+page.svelte` and `frontend/src/routes/person/[id]/+page.svelte`.

### Verification
- `go build`, `go vet ./...`, `npm run check` all pass with 0 errors.

### Lessons learned
- The original SSE implementation started polling from `id > 0` for fresh connections, which replayed the entire event history. This caused the frontend to fire `refreshPersons()` for every historical event, creating a request flood. The fix is simple: query `MAX(id)` on fresh connect so only genuinely new events are streamed.

## 2026-02-02: Infrastructure & Deployment (Phase 2)

### What was done
- **Dockerfile**: Multi-stage build (`golang:1.23-alpine` → `alpine:3.19`). Copies migrations to `/migrations` in final image. Entrypoint is `dm`.
- **`.dockerignore`**: Excludes `.env*`, `.git`, `node_modules`, `scraped_content`, built binaries.
- **Health check endpoint**: `GET /health` pings the database and returns `{"status":"ok"}` (200) or `{"status":"error","error":"..."}` (503). Placed outside `/api` group alongside `/metrics`.
- **CORS production config**: Reads `ALLOWED_ORIGINS` env var (comma-separated). If set, only those origins are allowed. If unset, falls back to localhost/127.0.0.1 for development.
- **Frontend adapter-vercel**: Switched from `@sveltejs/adapter-auto` to `@sveltejs/adapter-vercel` in `frontend/svelte.config.js`.
- **`.env.example` updated**: Replaced outdated Mistral AI section with OpenAI-compatible API config (`OPEN_AI_KEY`, `OPEN_AI_API`, model overrides). Added `ALLOWED_ORIGINS` and `PORT`.

### Verification
- `go build`, `go vet ./...`, `docker build -t dm .`, `docker run --rm dm web serve --help`, `npm run check` all pass.

### Lessons learned
- Docker build context must be run from project root (`docker build -t dm .`), not from a subdirectory.
- The `corsOriginFunc()` is evaluated once at server startup, so changing `ALLOWED_ORIGINS` requires a restart.

## 2026-02-01: Implement goose migrations

### What was done
- Added `github.com/pressly/goose/v3` dependency for database migration management.
- Created `supabase/embed.go` to export an embedded FS of all SQL migration files. This avoids the `go:embed` limitation where `..` paths are not allowed -- placing the embed directive in the same directory as the migrations (`supabase/`) solves this cleanly.
- Created `backend/migrate/migrate.go` which imports the embedded migrations from `supabase.Migrations` and runs them via goose.
- Created `cmd/helpers.go` with `ensureMigrations()` helper.
- Modified `cmd/root.go` PersistentPreRunE to:
  1. Load `.env` via godotenv (so DATABASE_URL is available before migrations run).
  2. Check for `"needs_db": "true"` annotation on the command and run migrations if present.
- Added `Annotations: map[string]string{"needs_db": "true"}` to all DB-dependent commands:
  - `serveCmd` (cmd/web.go)
  - `newsSearchCmd` (cmd/news.go)
  - `queueProcessCmd`, `queueAddCmd`, `queueRemoveCmd`, `queueListCmd` (cmd/news_queue.go)
  - `contradictionsDetectCmd` (cmd/contradictions_detect.go)
  - `ingestFilesCmd` (cmd/ingest_files.go)
- Created new migration `20260201000000_add_rss_feeds_table.sql` with goose Up/Down annotations.

### Lessons learned
- **Symlinks do not work with `go:embed`**. The Go toolchain explicitly rejects symlinked directories in embed patterns (`cannot embed file X: in non-directory Y`). The solution is to place the `//go:embed` directive in a Go file that lives alongside the files to embed (in this case, `supabase/embed.go` next to `supabase/migrations/`).
- Existing migrations (001_, 002_, 20250601...) do not have `-- +goose Up/Down` annotations. Goose will still run them as "up" migrations, but they won't support rollback. New migrations should include goose annotations.
- The godotenv.Load() calls in individual commands are now redundant since PersistentPreRunE loads .env first, but they were left in place to avoid unnecessary churn.
