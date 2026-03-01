# CHANGES.md

This file tracks high-level changes to the codebase, lessons learned, and mistakes to avoid.

## 2026-02-27: Separate Stance (canonical_verb) from Topic (embedding_text)

### What was done
- **New `canonical_verb` column**: Split the verb prefix out of `embedding_text` into its own indexed column. Migration adds `canonical_verb TEXT` with B-tree index and drops `topics TEXT[]`.
- **Verb-free embedding_text**: Embeddings now encode only the topic domain (e.g., `wrongdoing parliamentary-assistants-case`), not the stance. This makes contradictory facts (DENIED vs WAS_CONVICTED on same topic) cluster together in vector space.
- **Reasoning-first extraction prompt**: LLM now outputs `reasoning` as the first JSON field before committing to `canonical_verb` and other fields. Reasoning logged at debug level.
- **Verb-aware auto-dedup**: Insertion dedup now checks up to 5 similar facts. Same verb + high similarity → skip. Different verb → never skip (opposing stances must coexist).
- **Same-verb skip in contradiction detection**: Both CLI and real-time listener skip same-verb pairs with >0.85 similarity before calling the LLM. Reduces wasted LLM calls on paraphrases.
- **Batched listener**: Real-time contradiction listener now uses `AreContradictoryBatch` instead of per-pair calls.
- **Dedup LLM prompt**: Added pairwise `analysis` array for reasoning before grouping. `canonical_verb` included in fact JSON for verb-aware dedup. Default batch size reduced from 50 to 10.
- **Backfill CLI**: `dm facts backfill-verbs [--dry-run]` parses verb prefix from existing `embedding_text`, updates `canonical_verb`, strips verb from `embedding_text`, and re-generates embeddings.

### Key design decisions
- frpolitics plugin already used verb-free `embedding_text` (via `prepareLawPhrase`), validating that topic-only embeddings cluster related votes correctly.
- `Topics []string` column removed entirely — it was unused for any logic, and `canonical_verb` + topic-domain embedding replaces its purpose.
- `CanonicalVerb` is a simple string, not an enum — allows unknown verbs to pass through with a warning rather than failing.
- Backward compatibility: parser falls back to parsing verb from `embedding_text` first word when `canonical_verb` is empty.

## 2026-02-24: Real Counts + Exponential Honesty Score

### What was done
- **Paginated API responses**: `GetPersonFacts` and `GetPersonContradictions` now return `{items: [], total: N}` instead of bare arrays. Total count computed via CTE subselect in a single query (no extra round trip). Frontend tab labels show real total (e.g., "Facts (148)") instead of "25+".
- **Hybrid honesty score**: Formula is `exp(-4 * ratio - 0.02 * contradictions)` — combines ratio-based decay (k=4) with absolute contradiction penalty (j=0.02). This prevents high fact counts from diluting many contradictions. E.g., 31/500 → 42% (was 78% with ratio-only, was 93% with linear).
- **`dm scores recompute` CLI**: Bulk-recomputes all person scores in a single transaction. Supports `--dry-run`. One SQL query fetches all persons with their counts.

### Key design decisions
- CTE total uses `COUNT(DISTINCT id)` to count unique facts, not fact-source rows (a fact with multiple sources should count as one).
- `ComputeHonestyScore` is a pure function (no DB) for easy unit testing. Decay factors k=4 and j=0.02 are exported constants.
- Fallback count query runs only when the paginated page returns 0 rows (e.g., offset past end).

## 2026-02-23: Contradiction Detection — Debug flag, Gemini 2.5 Flash, batch=3, self-consistency check

### What was done
- **`--debug` flag on `dm contradictions detect`**: Prints LLM reasoning + verdict for every pair, not just contradictions. Uses parsed REASONING line with full raw response as fallback.
- **REASONING line parsing**: New `reasoningRegex` in `openai_detector.go` captures `PAIR N: REASONING: ...` lines from the reasoning-first prompt format. Stored in `BatchResult.Reasoning` field.
- **Model switch**: `OPEN_AI_CHAT_MODEL` changed from `google/gemini-2.0-flash-001` to `google/gemini-2.5-flash` (OpenRouter).
- **Batch size increase**: `MaxPairsPerBatch` increased from 1 to 3. Tested with Gemini 2.5 Flash — no cross-pair confusion, 3x cheaper.
- **Self-consistency check in fact extraction prompt**: Added to `CoreFactExtractionPrompt` to prevent self-contradictory `normalized_fact` text. Specifically targets negation chain misreads (e.g., "DENIED X lacks Y" when source says "accused X of lacking Y").
- **Deleted `cmd/contradictions_evaluate.go`**: Was created by mistake, never committed.

### Key design decisions
- Self-consistency check is an instruction-only approach (no second LLM pass). Cost-neutral. Can add validation pass later if needed.
- `google/gemini-2.5-flash` is the correct OpenRouter model ID (not `-preview`).
- Production run: 616 pairs checked, 33 contradictions found, 0 errors. ~15 minutes.

## 2026-02-19: Fix Dedup Pipeline — Threshold, Prompt, Model

### What was done
- **Auto-dedup threshold**: Lowered from 0.92 → 0.90 in both `fact_inserter.go` and `fact_deduplicator.go`. Catches ~42 more paraphrase pairs at ingestion time without LLM cost.
- **Dedup LLM prompt**: Aligned with extraction prompt's Type 1/Type 2 framework. Court rulings and judicial findings are now valid Type 2 facts (previously the dedup prompt rejected "X was found guilty" as invalid, contradicting the extraction prompt).
- **Model upgrade**: Changed `OPEN_AI_CHAT_MODEL` on the host from `mistralai/ministral-8b-2512` to `google/gemini-2.0-flash-001`. Ministral 8B was too weak for reasoning tasks (inconsistent embedding_text, poor dedup/contradiction judgment).

### Key design decisions
- Uncertain zone stays at 0.75-0.90 (not lowered to 0.60 — too noisy, and the relation classifier already handles 0.60+ at ingestion).
- Single model for all tasks (extraction, dedup, contradiction, classification) — Gemini Flash is cheap enough.

## 2026-02-18: Fix Fact Linking + Unify Contradiction Prompt

### What was done
- **`fact_inserter.go`**: Removed `involved_people` linking to `fact_mentions`. Previously both `subject_entity` AND `involved_people` were linked with no role distinction, causing facts about other people (Trump, Bardella, etc.) to be attributed to someone merely mentioned. Now only `subject_entity` creates a `fact_mentions` link.
- **`fact_extractor.go`**: Strengthened `subject_entity` field description to clarify it's the ACTOR/SPEAKER, not the person talked about. Added examples. Clarified `involved_people` won't be attributed the fact.
- **Unified contradiction prompt**: Merged `contradictionPromptTpl` (single pair, 12 examples) and `batchPromptTpl` (batch, 0 examples) into a single prompt with all 12 examples + batch `PAIR N:` format. `AreContradictory()` now delegates to `AreContradictoryBatch()`.
- **SQL cleanup**: Deleted 579 mislinked `fact_mentions` across all persons. Marine Le Pen went from 177 → 148 facts.

### Key design decisions
- `InvolvedPeople` field kept in `StoreFactInput` (useful metadata) but no longer creates DB links.
- Single prompt ensures batch and single-pair detection have identical quality (the old batch prompt had zero examples).

## 2026-02-16: Canonical embedding_text format

### What was done
- Redesigned `embedding_text` field in the fact extraction prompt from free-form native-language sentences to **canonical English fingerprints**: `CANONICAL_VERB [broad-policy-domain] [detail]`.
- Fixed vocabulary of 20 canonical verbs (DENIED, CLAIMED, PROMISED, VOTED_FOR, etc.) ensures consistent phrasing across extractions.
- Topic abstraction rule: use broadest policy domain so related positions cluster (e.g., "workers-social-rights" covers both "more financial freedom" and "reduce holidays").
- Always English — embedding model (text-embedding-3-small) clusters English tighter than French.
- Updated dedup LLM prompt example and test fixtures to match.

### Key design decisions
- `embedding_text` is ONLY used for pgvector cosine similarity (dedup at 0.92, contradiction neighborhood at 0.68). The LLM contradiction check uses `normalized_fact` (with names). So it's a semantic key, not display text.
- Canonical verbs eliminate synonym variation ("a nié" vs "a contesté" vs "a réfuté" → all become DENIED).
- Broad topic domains ensure contradictory positions land in the same neighborhood for detection.

## 2026-02-16: Simplify Fact Extraction Prompt

### What was done
- Rewrote `CoreFactExtractionPrompt` in `backend/core/fact_extractor.go` — ~290 lines → ~105 lines (~65% reduction).
- **Removed AUTO-REJECT TRIGGERS** section (40 lines of French keyword patterns like "risque", "pourrait", "espère"). These were asking the LLM to do regex matching, which is fragile and wastes context on a small model. The semantic rules already cover the same concepts.
- **Consolidated 9 numbered rules into 7 REJECT rules** — merged overlapping rules (courtroom behavior + biographical + non-contradictable into fewer rules), eliminated redundant examples.
- **Trimmed KEY TEST section** — removed 14 worked examples (concepts already covered by the rules), kept just the 4-question checklist.
- **Reduced output examples** from 3 JSON objects to 1.

- **Added "contradiction test" framing**: "Can you imagine a future headline where this entity does or says the OPPOSITE?" — makes the core purpose explicit to the model.
- **Added Type 1 / Type 2 valid facts**: Type 1 = entity's own positions/promises/denials (could later flip-flop). Type 2 = authoritative outcomes (court rulings, documented evidence, official votes). Explicitly excludes opinions without evidence from Type 2.
- **Updated Rule 1 and Rule 5** to align with the two-type system. Journalist reporting a documented finding with evidence is now explicitly valid (Type 2), while journalist narrative/opinion without evidence remains rejected.

### Key design decisions
- Semantic rules over keyword lists: the LLM should reject "X risque une peine" because it fails the "speculative/conditional" rule, not because the word "risque" appears. Keyword matching via LLM is inherently fragile.
- The "promise → break promise" framing makes the *purpose* of fact extraction clear to the model, so it can make better judgment calls on edge cases.
- One example output is sufficient for format compliance — 3 examples wasted ~30 lines of context.
- Fewer tokens in prompt = more room for article content in the context window, especially important for Ministral 8B.

## 2026-02-16: Auto-dedup Facts at Ingestion Time

### What was done
- **`FactInserter.insertSingleFact()`** now checks for near-duplicate active facts (via `FindSimilarActiveFacts`) before inserting. Uses `DEDUPE_AUTO_THRESHOLD` (default 0.92) — same env var as the batch `FactDeduplicator`.
- **`InsertFacts()`** returns `(*InsertResult, error)` with `Inserted` and `Skipped` counts. All 7 callers updated.
- **Removed `CheckNewFact()`** from `FactDeduplicator` — it was broken (searched by `InvolvedPeople` instead of `SubjectEntity`) and never called. The new inserter-level dedup replaces it.
- **Cleaned up `FileIngester`**: removed `deduplicator` field, `SetDeduplicator()`, and `SkipDedupeCheck` option since dedup now happens inside the inserter.

### Key design decisions
- Dedup errors are logged as warnings and don't block insertion — availability over consistency.
- Same threshold shared between ingestion-time dedup and batch CLI dedup via `getEnvFloat("DEDUPE_AUTO_THRESHOLD", 0.92)`.

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
