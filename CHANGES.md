# CHANGES.md

This file tracks high-level changes to the codebase, lessons learned, and mistakes to avoid.

## 2026-05-03: Stance is mandatory on every fact (single ingest path)

### What was done
- `core.NewFactInserter(storer, stance)` now requires a non-nil `StanceProcessor`. The optional `SetStanceManager` setter, the `DisableClassifier` toggle, and the silent fallback that ran `classifyRelations` when stance processing failed are all gone. Stance failure now triggers a compensating `DeleteFact` so the "every fact has a stance" invariant cannot be broken from this code path.
- `StanceProcessor` is a small interface (`ProcessNewFact`) implemented by `*core.StanceManager`. Tests can supply a stub that creates one stance per fact without an LLM round-trip — see `stubStanceProcessor` in `backend/storage/postgres_test.go`.
- All 11 `NewFactInserter` callsites (`cmd/{web,batch×3,pipeline,contradictions_detect,ingest_files,news}`, `plugins/{news,twitter,frpolitics}`, `ingester`) now construct a `core.StanceManagerFromEnv` and fail loudly with a clear error if `OPEN_AI_KEY`/`OPEN_AI_API` aren't configured. Previously they would silently fall through to the deleted relation-classifier path.
- `RelationClassifier` (the per-fact LLM call) and `classifyRelations` are deleted. The classification-prompt + JSON-parsing helpers (`ParseClassificationResponse`, `ClassificationInput`, `ClassificationResult`, `classificationPrompt`) stay because the OpenAI batch-API path in `backend/core/batch_job.go` still uses them. That batch path is now redundant with stance-based clustering and is queued for removal in a follow-up.

### Why
- A fact with no stance is a data-quality bug: contradiction bundling, the honesty score, and the topic UI all assume every fact lives in exactly one stance. The pre-existing code accepted facts via three different routes (FactInserter w/ stance, FactInserter w/o stance + classifyRelations, batch path), and the silent fallback meant any LLM hiccup produced stance-less rows. Collapsing to one path makes the invariant inspectable in a single function.
- Compensating delete on stance failure is the saga-pattern equivalent of a transaction. The storage layer doesn't expose a tx that spans LLM calls, so wrapping the fact-insert + stance-route in a Postgres `BEGIN/COMMIT` would mean holding a connection open across multiple seconds of LLM round-trips — not viable. End state is the same: partial inserts get rolled back.

### Operational notes
- Any caller that previously constructed `NewFactInserter(storer)` without LLM credentials now panics with a clear message. This is intentional: there is no "ingest without stance" mode anymore.
- Old facts predating this change may still have no `stance_document_facts` row. Run `./dm stances rebuild --resume` once per environment to backfill — the inserter only enforces the invariant going forward.

## 2026-05-03: `dm contradictions bundle-backfill`

### What was done
- New CLI subcommand to retroactively populate `bundle_stance_id` on existing contradictions. Two phases: (1) SQL fast path (mirrors the migration) — set `bundle_stance_id` where both facts now share a stance via `stance_document_facts`; (2) LLM Bundler pass — for remaining solos, embed the contradiction summary, retrieve top-K candidate stances per person, ask the LLM YES/NO. Flags: `--person`, `--person-id`, `--max`, `--no-llm`, `--dry-run`.
- New storage helpers `BundleBackfillFastPath(personID *int64)` and `ListSoloContradictionsForBundling(personID *int64, limit int)`.

### Why
- The migration's UPDATE only ran once at deploy time; it could only bundle contradictions whose facts already had stances. Older facts that predated the stance system stayed stance-less, so their contradictions stuck as solo bundles. After running `./dm stances rebuild --resume` to backfill stances, this command picks up the rest in one auditable pass instead of a one-off psql session.

## 2026-05-03: Stance-bundled contradictions

### What was done
- A contradiction's "bundle" is now the stance its facts share. New column `contradictions.bundle_stance_id` (migration `20260503000000_add_contradiction_bundle_stance.sql`) is filled at insert time via a CTE: when both facts of a contradiction belong to the same `stance_documents` row, that stance becomes the bundle id. Cross-stance and unstanced pairs are left NULL and treated as singleton "solo" bundles.
- `ComputeHonestyScore` signature is now `(factsCount int, stats ContradictionStats)` where `ContradictionStats{Bundles, Total}`. Formula adds a `0.05·ln(1+extras)` log penalty for repeats inside the same bundle, replacing the legacy linear `0.02·N` charge per pair. Effect: 10 contradictions sharing a stance count as ~1.135 instead of ~10 in penalty terms.
- `GetPersonContradictions` returns `bundle_id`, `bundle_label`, `bundle_summary`, `bundle_size`. It also returns `fact_a_stance_id/label` and `fact_b_stance_id/label` so cross-stance solos render with a "topic A ↔ topic B" header and a `Cross-topic` badge in the frontend; missing stance labels (a `facts_without_stance_total > 0` situation) show a red `Data quality` badge. Frontend (`person/[id]`) now groups contradictions by bundle, showing one card per bundle with the stance label/summary and an expander listing the underlying pairs. Solo rows render inline.
- New `backend/contradiction/bundler.go` + `embedding.PurposeContradictionBundle`: when a fact-level shared stance isn't found at insert (cross-stance or unstanced), the Bundler embeds the contradiction summary under a tailored "underlying inconsistency" instruction, retrieves top-K stances for the person via pgvector, and asks the LLM YES/NO whether each is the bundle. Wired into both sync (`cmd/contradictions_detect.go`) and batch (`cmd/batch.go`) paths. Best-effort: no LLM/embedder → row stays solo.
- SSE `contradiction_added` payload now carries `bundle_id`, `bundle_size`.

### Why
- Honesty was being penalised proportionally to *how many times the same broken promise was restated*, not to *how many distinct lies*. One subject (parliamentary-assistants on /person/28) produced ~10 contradiction pairs from the same underlying inconsistency, dragging the score with it. Bundling makes the score reflect distinct disagreements while still keeping all raw pairs as audit trail.
- Reusing stances rather than introducing a parallel "contradiction_groups" entity avoids duplicating per-person topic clustering that already runs at ingest. The Bundler+LLM fallback covers the legitimate case where contradicted facts genuinely live in different stances ("promise" vs. "retraction"), without false-merging unrelated topics.

### Operational notes
- Existing facts predating the stance backfill have no `stance_document_facts` row → their contradictions land as solo bundles. Run `./dm stances rebuild --parallel 8 --resume` once per environment to retroactively classify, or rely on the Bundler to upgrade rows lazily on next detection.
- The Bundler instructs the embedding model to project on the *underlying inconsistency* (broken promise / reversal / topical conflict) rather than the topic of either claim — this is a different `Purpose` from `fact_storage` and would need its own re-embed if its instruction changes.

## 2026-05-03: `dm contradictions check` audit tool

### What was done
- New CLI subcommand `./dm contradictions check` that re-judges already-recorded contradictions with the LLM and emits a per-row verdict (`keep` / `remove` / `ambiguous`). Read-only — never writes to the DB.
- Flags: `-p/--person`, `--max N`, `--random` (`ORDER BY RANDOM()` for sampled audits), `--format jsonl|text`.
- JSONL goes to stdout (jq-friendly), the summary block goes to stderr so pipes stay clean.
- Refactor: extracted the rule body + 26 examples from `contradictionPromptTpl` into a public `contradiction.ContradictionRulesAndExamples` constant, now consumed by both the detector prompt and the new auditor prompt — single source of truth for contradiction rules.
- New `backend/storage.ListContradictions(ctx, *personID, random, limit)` to walk contradictions across the whole DB (or scoped to one person) in a single query joining `contradictions` + `persons` + `facts` + `fact_sources`.

### Why
- The detector misses cases like accusations-vs-findings, paraphrase-same-position, and geographic-scope, so false positives accumulate in the `contradictions` table and pollute honesty scores. We needed a second-pass auditor to surface them, but writing verdicts back to the DB would bias future detection — keeping the tool diagnostic-only is the right scope. JSONL stdout makes it trivial to pipe into `jq` or feed back into Claude Code for follow-up triage.

## 2026-05-02: Matryoshka-truncate Qwen3-Embedding-8B to 1536 dims client-side

### What was done
- `backend/embedding/openai_embeddings.go` now truncates any embedding longer than `targetEmbeddingDim` (1536) to the leading-1536 prefix and L2-renormalizes. Qwen3-Embedding-8B was trained with nested Matryoshka loss, so the prefix is a valid lower-dim embedding.
- pgvector storage stays at `vector(1536)` — same column type that was used with `text-embedding-3-small`, no reindex / no schema rewrite when swapping the upstream model.
- New migration drops `NOT NULL` on `facts.embedding` so future model swaps can use the `embeddings invalidate` → `embeddings regenerate` flow without first having to alter the column.

### Why
- The OpenAI `dimensions` request parameter is silently ignored by llama.cpp (cachy returns 4096-dim regardless), so server-side truncation isn't an option. Doing it client-side keeps the wire format vanilla OpenAI and works against any upstream — OpenRouter fallback included, since OpenRouter also serves the full 4096-dim Qwen output.
- Storing 1536 vs 4096 dims is a ~2.7× saving in row size, HNSW index memory, and KNN scan cost — cheap, and avoids a full schema rewrite of every embedding-bearing table.

## 2026-05-01: LiteLLM transport switched from Tailscale to Cloudflare Tunnel

### What was done
- Stripped Tailscale (userspace tailscaled + SOCKS5/HTTP-CONNECT proxy + state volume) from the host `lite-llm` container. The Dockerfile is now just `ghcr.io/berriai/litellm:main-stable` + `start.sh` + `config.yaml`; `start.sh` is a 4-line `exec litellm`.
- Reach to the home AI box now goes the host → Cloudflare edge → Cloudflare Tunnel (cloudflared on cachy) → `localhost:8080` (llama-swap). cloudflared runs as a host systemd service on cachy, no Docker.
- Auth at the Cloudflare edge via Access service token: LiteLLM sends `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers, declared once per primary route in `config.yaml` via `extra_headers`. Fallback (OpenRouter) routes do not carry these headers.
- New env vars on the `lite-llm` the host service: `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET`. `OPEN_AI_API` flipped from the tailnet hostname to the public Cloudflare hostname (e.g. `https://cachy-llm.<domain>/v1`). `TS_AUTHKEY` / `TS_HOSTNAME` and the `/var/lib/tailscale` volume are removed.

### Why
- Tailscale userspace networking on the host is structurally unable to transport TCP responses larger than ~1KB regardless of proxy type or whether the path is direct UDP or DERP — gVisor netstack throughput limit, confirmed via tailscale#9802 and reproduced across `--tun=userspace-networking`, `tsnet` (railtail), SOCKS5, and HTTP CONNECT. Embedding payloads (~6KB) and >50-token chat completions all timed out. The data path had to leave Tailscale.
- Cloudflare Tunnel is outbound-initiated from the home box, so it traverses NAT without router config, terminates TLS at CF edge with a free cert, and lets us put auth at the edge with Access. Cost: a domain on CF Registrar (~$10/yr) — the only realistic alternatives required either a port-forward + reverse-proxy auth shim on cachy (Caddy/nginx) or a paid VPS as a subnet router.

### Operational notes
- cloudflared logs `dial tcp [::1]:8080: connect: connection refused` if the tunnel route is configured as `localhost:8080` and the origin only binds IPv4. Use `127.0.0.1:8080` in the route to force IPv4.
- The Access app must use **Service Auth** policy action (not Allow) — Service Auth gates by service token only, no human login redirect, which is what a server-to-server call needs.
- Rotating the Access service token: regenerate it in Zero Trust → Service Tokens, update both the host env vars, and re-bind the new token in the Access policy. The hostname and tunnel itself don't change.

## 2026-04-29: LiteLLM proxy + Tailscale on the host for home AI box failover

### What was done
- New host service `litellm` (under `deploy/litellm/`) running LiteLLM proxy with a Tailscale userspace sidecar in the same container. Routes LLM and embedding traffic from `dm` to a home AI box via tailnet, with automatic fallback to OpenRouter when the home box is unreachable.
- **Backend Go code is unchanged** — `dm` still reads `OPEN_AI_API` and behaves identically. On the host, that variable now points at `http://litellm.host.internal:4000/v1`; locally it still points at the cloud provider directly. No new branches, no new dependencies.
- New env vars on the `litellm` service only (5 total): `TS_AUTHKEY`, `TS_HOSTNAME`, `FALLBACK_URL`, `FALLBACK_KEY`, `FALLBACK_CHAT_MODEL`. Embedding fallback intentionally reuses `OPEN_AI_EMBED_MODEL` so the same model name resolves on both providers — drift is structurally impossible.

### Key design decisions
- **LiteLLM over hand-rolled Go failover**: replaces ~175 lines of Go (circuit breaker + retry + dispatch) with a Python sidecar configured via YAML. Failover, retries, cooldown, and per-provider auth all live in `deploy/litellm/config.yaml`.
- **Tailscale lives only in the LiteLLM container**, not on `dm`. Userspace mode (`--tun=userspace-networking`) because the host containers can't access `/dev/net/tun`. The container's outbound HTTP routes through tailscaled's HTTP proxy on `localhost:1055`, which forwards public traffic transparently — so OpenRouter fallback works through the same proxy without special-casing.
- **No public domain on the LiteLLM service** + no `LITELLM_MASTER_KEY`: the host's private network is project-scoped, so the proxy is unreachable from outside the project. Documented as a hard deploy-time requirement.
- **Single source of truth for embedding model name**: both primary and fallback YAML routes reference `os.environ/OPEN_AI_EMBED_MODEL` (not separate env vars), making vector-space drift between providers impossible. Operationally requires `ollama cp <tag> qwen/qwen3-embedding-8b` on the home box once so the same string resolves on both Ollama and OpenRouter.
- **Embedding model swap requires pgvector reindex**: switching from OpenAI's `text-embedding-3-small` to a Qwen embedding produces incompatible vectors. Existing index must be cleared and re-ingested when this change rolls out.

### New CLI commands for the embedding swap
- `dm embeddings invalidate --confirm` — single transaction: nulls every `facts.embedding` and deletes every `stance_documents` row. Refuses to run without `--confirm` and then asks for an interactive `yes`. Cascade on `stance_document_facts` cleans up the join rows.
- `dm embeddings regenerate [--batch-size=50]` — walks `WHERE embedding IS NULL` ordered by id, batches through `embedding.GenerateEmbeddings`, writes per-row via `storer.UpdateFactEmbedding`. Idempotent and resumable. Source text per fact is `COALESCE(NULLIF(embedding_text, ''), fact_text)`. After it completes, run `dm stances rebuild --all` to rebuild stance documents from the new vectors.

### Embedding interface now requires a Purpose
- `EmbeddingGenerator.GenerateEmbeddings(ctx, purpose, texts)` — added `embedding.Purpose` argument. The OpenAI generator looks up an instruction string per purpose and prepends Qwen's `Instruct: ...\nQuery: ...` format to every input. text-embedding-3-small ignores the prefix harmlessly during the cutover.
- One purpose for now: `PurposeFactStorage`. Every callsite stores the resulting vector in `facts.embedding` and KNN searches reuse those stored vectors as queries (symmetric retrieval). All ingestion paths and re-embed CLIs were updated to pass it.
- Stance cluster vectors are not affected: `stance_documents.cluster_embedding` is the running centroid of fact embeddings (`updateCentroid` in `backend/core/stance_manager.go`), so it inherits whatever instruction the underlying facts were embedded with.

## 2026-04-10: Mistral BatchProvider (alternative to Gemini)

### What was done
- New `llm.MistralBatchProvider` implementing `llm.BatchProvider` alongside the existing `GeminiBatchProvider`. Uses Mistral's file-based batch flow: upload JSONL to `/v1/files` (purpose=batch) → create job via `/v1/batch/jobs` → poll → download output JSONL from `/v1/files/{id}/content`.
- Default model pinned to `mistral-small-2603` (Mistral Small 4, released 2026-03-16). We do **not** use the `mistral-small-latest` alias because it currently aliases to Devstral Small 2, a coding-focused variant. Override via `MISTRAL_BATCH_MODEL` if needed. Reuses `ChatMessage`/`ResponseFormat`/`ChatResponse` directly (OpenAI-compatible schema), so no custom converter is needed unlike the Gemini path.
- Provider selection: `BATCH_PROVIDER=mistral` picks Mistral, `BATCH_PROVIDER=gemini` picks Gemini. Both `GEMINI_API_KEY` and `MISTRAL_API_KEY` can coexist in the environment — only the one matching `BATCH_PROVIDER` is read. Switching providers on the host is a one-variable change; no commenting, no deletions, no startup panics.
- New env vars: `MISTRAL_API_KEY` (required when selected), `MISTRAL_BATCH_MODEL` (optional override).

### Key design decisions
- **File-based over inline batching**: Mistral supports both, but file-based is the primary documented flow and scales past the 10k inline cap. The extra upload/download round-trips are fine for our daily batch cadence.
- **Error file merge**: `Retrieve` downloads `error_file` (if present) in addition to `output_file` and merges errored items by `custom_id`, keeping failure detail visible to the orchestrator rather than silently dropping them.
- **No orchestrator changes**: `BatchJobOrchestrator`, the poller, the recovery CLI, and contradiction detection all depend only on `llm.BatchProvider`, so they pick up Mistral for free.

## 2026-03-23: Fix the host Pipeline — Batch Dedup, Circuit Breaker, Observability

### What was done
- **Batch dedup + persist-before-submit**: `PrepareAndSubmit`, `SubmitContradictionBatch`, `SubmitClassificationBatch` now (1) dedup by customID before submission, (2) persist batch + requests to DB *before* submitting to provider. On submit failure, batch is marked `"failed"` in DB instead of orphaned. New `UpdateBatchProviderID` method updates the real provider ID after successful submit. Poller skips batches with `provider_batch_id = "pending"`.
- **Brave API circuit breaker**: After 3 consecutive 429s, circuit opens for 1 hour — all subsequent Brave calls return immediately instead of burning 30s in retries. Resets on first successful response.
- **SerpAPI web search fallback**: `NewWebSearcherFromEnv()` falls back to SerpAPI (engine=google) when Brave key is missing. New `SearchWeb()` method on `SerpAPINewsProvider`.
- **Pipeline zero-content warning**: Telegram notification shows ⚠️ when targets were processed but 0 content items collected (instead of misleading ✅).
- **Balance log noise**: Free-tier/limit-0 accounts now log at DEBUG instead of INFO (eliminates 22 lines/day of OpenRouter noise).
- **Batch recovery CLI**: `dm batch recover <provider-batch-id>` recovers orphaned batches — creates missing DB records, then processes through normal pipeline.

### Key design decisions
- Persist-before-submit eliminates orphaned batches: if provider submit fails, the batch is tracked in DB as `"failed"` and can be retried. The `"pending"` provider_batch_id sentinel is skipped by the poller.
- Circuit breaker is package-level (shared across all BraveSearchProvider instances), same pattern as the existing rate limiter.
- SerpAPI `SearchWeb` uses `engine=google` (organic_results), not `google_news`.

## 2026-03-20: Batch Chain — fact_extraction → classification → contradiction

### What was done
- **Batch classification**: After batch poller processes a `fact_extraction` batch, it auto-submits a `classification` batch for all newly inserted facts. Uses existing `classificationPrompt` from `relation_classifier.go`. Results write `fact_relations` rows (duplicate/implication).
- **Batch chain**: When a `classification` batch completes, checks if any sibling classification batches (same parent extraction batch) are still pending. If none, auto-triggers contradiction detection via `contradictionSubmitter` callback.
- **Removed contradiction detection from daily pipeline**: No longer needed — the chain handles it: fact_extraction → classification → contradiction, triggered sequentially by the batch poller.
- **Deleted realtime listener** (`contradiction/listener.go`): Was dead code — never started anywhere in cmd/. Removed file + test.
- **InsertResult now returns fact metadata**: `InsertedMeta []InsertedFactMeta{FactID, PersonID}` propagated from `insertSingleFact` up through `InsertFacts` → `insertFactsFromBatch` → `processFactExtractionBatch` → `RetrieveResult.InsertedFacts`.
- **ParseClassificationResponse** exported from `relation_classifier.go` for reuse by batch classification processing.
- **CountUnfinishedBatchesByMetadata** on storage: queries `llm_batches` by job_type + metadata JSONB key-value, used for chain coordination.

### Key design decisions
- Classification uses `parent_extraction_batch_id` in batch metadata to track which extraction batch spawned it. Chain completion check queries this.
- `contradictionSubmitter` callback on orchestrator bridges core→cmd: the orchestrator (in `core`) triggers `executeContradictionDetection` (in `cmd`) without import cycle.
- Cycle prevention (`IsAncestorOf`) skipped in batch classification — implications are rare and can be cleaned up post-hoc.
- `SubmitClassificationBatch` returns 0 (not error) when no similar facts found — chain simply stops, which is correct (no new facts to classify = no contradictions to find).
- Sync classifier path (`classifyRelations`) kept for non-batch inserters (CLI commands).

## 2026-03-19: Reduce LLM Costs — Batch Everything

### What was done
- **Phase 1: Disable relation classifier for batch facts**: Added `DisableClassifier()` method to `FactInserter`. Called in batch poller (`cmd/web.go`) and pipeline inserter (`cmd/pipeline.go`). Eliminates 1 sync LLM call per batch-retrieved fact (~$8-9 savings per run).
- **Phase 2: Twitter to batch Gemini**: Added `CollectContent()` method to Twitter `Provider` — fetches tweets without LLM calls, returns `BatchContentItem` slices. Pipeline's `collectAndSubmitBatch` now also collects Twitter content and includes it in the Gemini batch. Sync Twitter collection (`runTwitterCollection`) skipped when `BATCH_PROVIDER` is set.
- **Phase 3: Contradiction detection to batch Gemini**: New `contradiction/batch_detector.go` with `PrepareContradictionBatch`, `ParseContradictionCustomID`, `ProcessContradictionResponse`. Extended `BatchJobOrchestrator` with `SubmitContradictionBatch` method, `SetContradictionHandler`, and job-type dispatch in `RetrieveAndProcess`. Refactored `contradictions_detect.go`: extracted `collectPairsForPerson`, added `submitContradictionBatch` (batch path) vs `processSyncContradictions` (sync fallback). Batch mode auto-selected when `BATCH_PROVIDER` is set (and not --debug/--dry-run).
- **Phase 4: Prompt fix + false positive cleanup**: Added `GEOGRAPHIC/JURISDICTIONAL SCOPE` rule + PAIR 26 example to contradiction prompt. Migration deletes 18 false positive contradiction IDs.
- **`BatchContentItem.PluginInstructions`**: New field passed through to `FactExtractorConfig` in batch job preparation, enabling plugin-specific prompts (used by Twitter).

### Key design decisions
- `DisableClassifier` is opt-in per inserter instance — doesn't affect non-batch callers.
- Twitter `CollectContent` reuses the same tweet filtering/dedup logic as `CollectFacts` but stops before LLM calls. Batches tweets by `BatchSize` (default 20) into content items.
- Contradiction batch custom ID encodes `c:{personID}:{factA}:{factB},...` for stateless result processing by the poller.
- `RetrieveAndProcess` dispatches on `batch.JobType` ("fact_extraction" vs "contradiction") — the two result handlers are structurally different (fact parsing vs verdict parsing).
- Sync contradiction path remains as fallback for --debug and --dry-run modes where immediate output is needed.

## 2026-03-19: Twitter in Daily Pipeline + Auto-Discovery

### What was done
- **Twitter in daily pipeline** (`cmd/pipeline.go`): Two new steps between batch news and contradiction detection:
  - Step 1.5: `runTwitterDiscovery()` — auto-discovers handles for persons without one
  - Step 1.6: `runTwitterCollection()` — fetches tweets for all linked handles
  Both skip gracefully if API keys are missing. Errors included in Telegram notification.
- **`dm plugin twitter discover`** (`cmd/twitter.go`): Auto-discovers Twitter handles. Uses Brave Web Search API (`/res/v1/web/search`) with `site:x.com` queries, extracts candidate @handles from URLs and snippets, confirms with LLM. Supports `--dry-run` and `--person`.
- **`SearchWeb` on BraveSearchProvider** (`backend/search/brave.go`): New method + `WebSearcher` interface for general web search (vs news-only). News search was useless for finding profile pages.
- **Seed script**: Uses `dm plugin twitter discover` instead of hardcoded handles — works for any person set.
- **Extracted `executeTwitterDiscovery()`**: Shared function called by both CLI and pipeline. `TwitterDiscoveryOpts` struct for configuration.

### Key design decisions
- Web search over news search: `site:x.com` queries find actual profile URLs. News search only found handles incidentally mentioned in articles (tested: returned 1 wrong candidate vs 4 correct ones with web search).
- Discovery runs before collection in the pipeline so newly discovered handles get tweets fetched in the same run.
- LLM confirmation (temperature 0.1) correctly rejects false positives (tested: rejected `@rafaelsereti` for Marine Le Pen, confirmed `@MLP_officiel`).

## 2026-03-16: Pipeline Scheduler + Seed Script for MVP

### What was done
- **Pipeline scheduler** (`cmd/pipeline.go`): Background goroutines started alongside the web server, same pattern as `StartBatchPoller`. Two independent schedules:
  - **Daily pipeline** (`PIPELINE_INTERVAL_HOURS`, default 0=disabled): Runs `queue process --plugins news` then `contradictions detect` as a catch-up pass.
  - **Frpolitics pipeline** (`FRPOLITICS_INTERVAL_DAYS`, default 0=disabled): Runs `frpolitics --mode both` (votes + news for deputies).
- **Extracted reusable functions**: `executeQueueProcess(ctx, storer, opts)` and `executeContradictionDetection(ctx, storer, opts)` extracted from CLI handlers into standalone functions. Both CLI commands and scheduler call the same code path.
- **Atomic skip-if-running**: Each pipeline uses `atomic.Bool` to skip the tick if the previous run is still in progress.
- **Telegram notifications**: Pipeline completion/errors optionally sent via Telegram (reuses existing `TelegramNotifier`).
- **Seed script** (`scripts/seed.sh`): One-time setup for MVP — adds 7 politicians + 3 parties, queues humans for news search, auto-affiliates via Wikidata, fetches photos.
- **Env vars**: `PIPELINE_INTERVAL_HOURS` (set to 24 for daily), `FRPOLITICS_INTERVAL_DAYS` (set to 15 for biweekly).

### Key design decisions
- Frpolitics already has `frpolitics.Run(ctx, storer, opts)` as a standalone function — scheduler calls it directly with default options.
- Queue process and contradiction detect needed extraction from cobra command handlers into `QueueProcessOpts`/`ContradictionDetectOpts` structs.
- Errors are logged, not fatal — next tick retries automatically.

## 2026-03-15: Telegram Alerts for LLM Credit/Balance Warnings

### What was done
- **`backend/alerting/` package**: Interfaces (`BalanceChecker`, `Notifier`), `TelegramNotifier` (HTML parse mode), `OpenRouterChecker` (GET `/auth/key`), `Alerter` orchestrator with per-provider cooldown dedup.
- **Proactive monitoring**: `StartBalancePoller` runs periodic balance checks (default 60min). Alerts via Telegram when balance < threshold (default $2).
- **Reactive detection**: `OnCreditError` callback in `llm/client.go` and `llm/batch_gemini.go` fires on 402, `insufficient_credits`, `RESOURCE_EXHAUSTED`, etc. Wired to Telegram via the same cooldown-aware alerter.
- **CLI**: `dm balance check [--alert]` prints balance table, optionally sends Telegram alert.
- **Prometheus gauge**: `digital_mirror_llm_balance_usd` by provider, updated on each check.
- **Env vars**: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `BALANCE_ALERT_THRESHOLD_USD`, `BALANCE_CHECK_INTERVAL_MIN`, `BALANCE_ALERT_COOLDOWN_HOURS`.

### Key design decisions
- Cooldown is per-key (provider + alert type), so low-balance and credit-error alerts have independent dedup windows.
- Failed Telegram sends don't update cooldown — retries on next cycle.
- Free tier and unlimited (limit=0) accounts skip alerting.
- `IsCreditError` is both in `alerting` package (for tests) and as `isCreditError` in `llm` package (to avoid import cycle).

## 2026-03-13: Batch Processing Interface for LLM Fact Extraction

### What was done
- **`BatchProvider` interface** (`backend/llm/batch.go`): Provider-agnostic batch API abstraction (Submit/Status/Retrieve). Types: `BatchRequest`, `BatchSubmitResult`, `BatchStatus`, `BatchResponseItem`.
- **Gemini provider** (`backend/llm/batch_gemini.go`): First implementation using Gemini's `batchGenerateContent` endpoint with inline requests. Handles state mapping (JOB_STATE_* → our BatchState), inline + file-based result retrieval.
- **DB tables** (`llm_batches` + `llm_batch_requests`): Persist batch state, per-request metadata (source_url, target, chunk_index), and response JSON. Survives the host redeploys.
- **Storage layer** (`backend/storage/batch.go`): CRUD for batches/requests — Create, Get, Update, List, MarkProcessed.
- **`BatchJobOrchestrator`** (`backend/core/batch_job.go`): Prepares prompts from cached content (with chunking), submits via provider, retrieves results and processes through existing `ParseFactJSON` → embedding → `FactInserter` pipeline.
- **CLI** (`cmd/batch.go`): `dm batch {submit, status, retrieve, list}` commands with `--dry-run` support.
- **Exported `ParseFactJSON`**: Renamed from `parseFactJSON` in categorizer for reuse by batch pipeline. Injected via `FactParser` func type to avoid core↔categorizer import cycle.

### Key design decisions
- `FactParser` function injection instead of direct import breaks core↔categorizer cycle.
- DB over files for batch state: multiple workers, survives deploys, integrates with `job_metrics`.
- `batchStorerAdapter` in cmd layer bridges storage↔core types (same pattern as stance adapter).
- Env vars: `BATCH_PROVIDER` (gemini|empty), `GEMINI_API_KEY`, `GEMINI_BATCH_MODEL` (default: gemini-2.5-flash).

## 2026-03-10: Stance Document System for O(1) Contradiction Detection

### What was done
- **Stance documents**: New `stance_documents` + `stance_document_facts` tables that compress all facts on a topic into a single summary per person. Each stance has a centroid embedding, summary text, key fact IDs, and fact count.
- **StanceManager** (`backend/core/stance_manager.go`): Core logic — `ProcessNewFact` finds the closest stance by embedding similarity (threshold 0.35), then either creates a new stance (no LLM) or makes ONE LLM call that returns DUPLICATE/NEW_INFO/CONTRADICTION verdict with updated summary.
- **Fact inserter integration**: Optional `StanceManager` wired into `FactInserter` — when set, replaces `classifyRelations` for contradiction detection. Falls back to old behavior if stance manager is nil or fails.
- **KNN cap**: `LIMIT 30` on `FindNeighborFactsForPerson` as immediate safety net independent of stance system.
- **CLI**: `dm stances {backfill, list, stats, rebuild}` — backfill clusters existing facts by embedding_text similarity, calls LLM for multi-fact summaries. Rebuild deletes + re-backfills.
- **Removed**: Fact improvement feature from dedup pipeline (unused complexity).

### Key design decisions
- Stance matching uses cosine distance 0.35 (tunable via `STANCE_MATCH_THRESHOLD` env var) — tight enough to avoid merging unrelated topics, loose enough to catch same-topic paraphrases.
- Single LLM prompt handles dedup + contradiction + summary update — one call per new fact instead of O(N) pairwise comparisons.
- Centroid updated via weighted average `(old * count + new) / (count + 1)` with unit normalization — drifts toward the topic center as facts accumulate.
- Backfill clusters by exact `embedding_text` match first, then merges clusters within cosine distance 0.2.
- No new env vars required (reuses OPEN_AI_* vars). Optional `STANCE_MATCH_THRESHOLD` for tuning.

## 2026-03-09: Auto-discover Party Affiliations via Wikidata

### What was done
- **`dm persons affiliate [--dry-run]`**: New CLI command that looks up each human person on Wikidata (property P102 = member of political party) and creates `person_links` affiliations with matching parties in the DB.
- **`backend/search/wikidata.go`**: Wikidata API client — searches entity by name (language=fr), fetches P102 statements, filters out ended affiliations (P582 qualifier), gets French labels for party matching.
- After creating affiliations, automatically calls `RecomputeAllHonestyScores` to propagate to party `members_score`.

### Key design decisions
- No new env vars — Wikidata API is free and keyless.
- 1-second delay between lookups to respect Wikidata rate limits.
- Fuzzy matching via `ResolveTargetName` with 0.7 threshold for party name matching.
- Idempotent via `FindOrCreateAffiliation` (ON CONFLICT upsert).

## 2026-03-06: Allow Dedup Across External Verbs + Fix Batch Deduplicator

### What was done
- **Relaxed verb-aware dedup for external verbs**: Facts with different external verbs (WAS_SENTENCED, WAS_CONVICTED, WAS_FINED, WAS_ACQUITTED, RECEIVED_FUNDS) on the same topic are now deduped at insertion time if similarity > 0.85. Previously "different verb = never skip" preserved all of them, causing 6 copies of the same conviction event.
- **Same filter in contradiction detection**: Both CLI (`contradictions_detect.go`) and real-time listener (`listener.go`) now skip both-external pairs with >0.85 similarity before LLM call.
- **Fixed batch deduplicator** (`core/fact_deduplicator.go`): Added verb-aware logic to Tier 1 auto-dedup (same verb OR both-external → dedup, different direct-claim → skip). Added `SetThresholds()` method so CLI `--auto-threshold` flag actually works (was being ignored, reading from env var only).
- **Fixed goose Down blocks in Supabase migrations**: 6 migration files had `-- +goose Down` sections that Supabase executed as plain SQL (it doesn't understand goose annotations), undoing the Up changes. Removed Down blocks.

### Results
- MLP: 25 facts deduped (all both_external=true), active facts 442→246, contradictions 41→28
- Total contradictions across all persons: 36 (down from ~50+)

### Key design decisions
- `isDirectClaimVerb()` duplicated as a local helper in `core/fact_inserter.go` AND `core/fact_deduplicator.go` to avoid import cycle (`core` ↔ `categorizer`).
- Direct claim verb pairs (DENIED vs CLAIMED) remain untouched — "never skip" still applies.
- Supabase migrations should NEVER include `-- +goose Down` blocks.

## 2026-03-05: Hybrid Entity Honesty Scoring

### What was done
- **New columns**: `own_score` (entity's own honesty) and `members_score` (average of active members' scores) on `persons` table.
- **Aggregate formula**: `honesty_score = 0.5 * own_score + 0.5 * members_score` for non-human entities. Humans use `own_score` directly.
- **Cascade**: When a person's score changes, all parent entities (via `person_links`) are recomputed automatically.
- **Two-pass recompute**: `RecomputeAllHonestyScores` first writes all `own_score` values, then computes `members_score` for non-human entities using the updated values.
- **API**: `PersonSummary` now includes `own_score` and `members_score` in all endpoints.

### Key design decisions
- `ComputeAggregateScore` is a pure function for testability, separate from DB logic.
- `computeMembersScore` uses `person_links` with `valid_to` filter for temporal correctness.
- All queries returning `PersonSummary` updated (GetPersonsRankedByScore, GetPersonByID, GetFactPersons, GetPersonsWithoutImage).

## 2026-03-05: Filter External-Only Pairs in Contradiction Detection

### What was done
- **Verb classification**: Added `IsDirectClaimVerb()` in `categorizer/fact_parser.go`. Splits 20 canonical verbs into direct (15: DENIED, CLAIMED, etc.) vs external (5: WAS_SENTENCED, WAS_CONVICTED, WAS_ACQUITTED, WAS_FINED, RECEIVED_FUNDS). Empty/unknown verbs treated as external.
- **CLI filter** (`cmd/contradictions_detect.go`): After pair dedup, skips pairs where neither fact has a direct claim verb. Uses existing `CanonicalVerb` from `FactWithEmbedding`.
- **Listener filter** (`backend/contradiction/listener.go`): Same logic in the real-time path, after the same-verb skip check.

### Key design decisions
- Rule: at least ONE fact in a pair must be a direct claim/action by the person. Two external attributions (e.g., court ruling vs journalist characterization) don't reflect the person's own dishonesty.
- Motivated by false positive: RN scored as dishonest because a journalist and a court disagreed on an embezzlement amount — neither was RN's own claim.

## 2026-03-04: Fix Fact Deduplication Pipeline

### What was done
- **Auto-dedup threshold**: Lowered from 0.90 → 0.85. Data analysis showed 97 same-verb pairs at distance 0.10-0.15 were virtually all genuine duplicates.
- **Self-initialized classifier**: `NewFactInserter` no longer takes a `classifier` parameter. It calls `NewRelationClassifierFromEnv()` internally with graceful degradation (nil if env vars missing). All 7 callers simplified.
- **Duplicate filtering in API**: `GetPersonFacts` and `GetPersonsRankedByScore` now exclude facts marked as duplicates via `fact_relations` table (same pattern as `FindSimilarActiveFacts`).
- **Classification prompt improvement**: Added `canonical_verb` to both new and existing fact context. Added concrete political examples (same claim from different articles = duplicate, same topic but different stance = unrelated).

### Key design decisions
- Classifier self-initialization keeps the pipeline self-contained — callers don't need to know about LLM configuration.
- `FILTER (WHERE NOT EXISTS ...)` on the `COUNT` aggregate in `GetPersonsRankedByScore` avoids a subquery or extra join.

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
