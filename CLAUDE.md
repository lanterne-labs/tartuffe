# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Digital Mirror is a fact-checking and honesty-scoring system for political figures. It extracts facts from web content and political data sources, stores them with semantic embeddings, and uses LLM-based contradiction detection to compute honesty scores.

**Tech Stack**: Go 1.24 backend (Chi router, pgx/PostgreSQL, pgvector), SvelteKit 5 frontend, Supabase for local development.

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
./dm facts refetch-articles --apply             # Read again the news pages whose text was not kept (before 2026-10-30) and store it: the date finder
                                                # and the explanation writer read the article around a quote
./dm facts statement-dates --apply              # Date the statements the text or the article dates before their source (a 2013 quote in a 2026 article);
                                                # --recheck-before <RFC 3339> reads again, with their article, the facts left without a date
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
# DETECT_REJUDGE_BUDGET=4500 on the worker service: how many negative pair verdicts of an older rulebook a pass
#   re-opens for the detector (default 0: a rule change keeps the old NOs; set it when a change is meant to find more)
# MAINTENANCE_EVERY_HOURS=24 on the worker service: the longest the maintenance pass waits while tasks are pending
#   (default 6; it runs as soon as the queue is empty after facts were added, whatever the value). Each pass takes the
#   seats for 2-3 h: a longer wait gives them to the extraction of the backlog (24 since 2026-11-11)
./dm persons candidate add "Marine Le Pen"       # the candidates: the persons the AI work after extraction runs for
./dm persons candidate list
```

**The API key's day** (`backend/worker/budget.go`): while every seat is at its limit, the API key pays and the split decides the order instead of the 6 h rule: a round of extraction (the feed and the tasks) spends `LLM_EXTRACTION_SHARE` of what is left of the day, then the maintenance pass runs, then what is left starts the next round. While the feed's plugin backfills (old transcripts of every deputy) have collection tasks pending ahead of them, they wait: the candidates' collection goes first. The Twitter backfill has priority 30, above the news backfills (10, 20), below the watch list (50).

**The candidates** (`persons.attributes.candidate`, `backend/storage/candidates.go`; the presidential candidates since 2026-11-01) are the persons whose facts get the AI work after extraction: the grouping gate, a stance, and the maintenance pass (refresh, merge, fold, pairs, detection, explanations, translations) run for them only. The facts of everyone are still extracted and stored (the feed's votes and transcripts of every deputy), but a fact of another person gets no stance and no contradiction detection, and the site says so. The queue is theirs: `dm collect add` and `dm queue add` refuse another person unless `--not-candidate`. The API lists them with `/api/persons?candidate=true` (`false`: the others).

The worker (`backend/worker`) replaces the nightly cron. It runs the work in the order of priority. The daily feed after 03:00 UTC (frpolitics votes and transcripts, the backfills for 1 h, the watch list tasks) goes before every task: it runs beside the tasks as soon as it is due, and while it is due or runs no backfill task starts and no maintenance pass (only the watch list's tasks, added at the start of the feed, are claimed; the backfills already running go on). The watch list tasks (the day before) have priority 50, above the backfills (`--priority`, 0 to 20). Within a priority, a task of a person no task runs for goes first (the facts of one person are inserted one at a time). The maintenance pass (stance refresh, stance merge, the fold of the split claim groups of the persons whose facts changed, contradiction detection over the persons with new facts, duplicate repoint, regroup, audit every 5 days, the explanations of the contradictions, translations, then every stored score written again at the candidates' new rate) runs alone, without a task or the feed, since it rewrites the stances they insert into: when no task is pending after facts were added, and at the latest every `MAINTENANCE_EVERY_HOURS` (6 by default, 24 on the worker since 2026-11-11) although tasks are pending (the worker stops claiming, lets the running tasks end, runs the pass, then goes on: a due pass goes before a new task, so free lanes do not fill first; while a feed is due or runs, the pass waits for its end). The pass judges what changed since the start of the last one (`job_metrics`, job `maintenance`). Its stance merge makes at most 2000 LLM calls, the persons at once as the seats serve, its fold at most 2000 grouping gate calls (a pair the gate judged at insertion or in an earlier fold keeps its stored verdict, `occurrence_checks`), and its detection judges at most 2000 pairs (about 2 h); a pass cut there records `cut_since_unix`, and the next pass goes on over the same persons once the tasks and the feed left room.

### Pre-polls (presidential election)
```bash
./dm poll list                                              # events, state
./dm poll event set presidentielle-2027 --min-voters 50 --featured   # create/change an event (flags given only)
./dm poll candidate add presidentielle-2027 "Marine Le Pen" "Jordan Bardella"
./dm poll candidate remove presidentielle-2027 "Name"
./dm poll blackout add presidentielle-2027 --from 2027-04-15T22:00:00Z --to 2027-04-18T18:00:00Z
./dm poll isoloir-setup presidentielle-2027 --id 6 --registry 1 --pk <hex> --sign-key <hex> \
    --circuit rankedm12_single_linked_d20 --vk-hash <hex>   # prints the isoloir poll JSON for `isoloir submit poll`
./dm poll purge-users --provider noop --yes                 # drop the test accounts
```

An event (`poll_events`, seeded: `presidentielle-2027`) lists candidates among the tracked persons, its blackouts and its minimum of voters. The ballots, the log and the tallies are **isoloir** (see "The anonymous vote" below): the Go backend only serves the event, its candidates' cards and its blackouts (`GET /api/polls/<slug>`). `dm poll isoloir-setup` turns an event into the isoloir poll: the options are the candidates sorted by name, then the blank.

The page `/election/[slug]/voter` shows the ranking (the slots ordered by the head-to-head wins of the tally's margins, the Condorcet winner marked) with the first choices, the honesty score /100 of each candidate (red to green), and the blank vote as its own line; results are hidden during a blackout (polling law: the day before and the day of a vote). `/election/[slug]/fonctionnement` tells the voter how it works; every line there must stay true to the code.

Rate limits key by client address: behind the host's edge that is `X-Real-IP` (the edge overwrites the client's own), elsewhere the connection's address (`CLIENT_IP_HEADER` names another header, `none` turns it off). `RATE_LIMIT_PER_MINUTE` (600: a page view makes about ten requests, and a mobile carrier puts many users behind one address) applies to every route, `SIGNIN_RATE_LIMIT_PER_MINUTE` (30) to the challenges and sign-ins together.

Accounts (`backend/accounts`, `AUTH_PROVIDERS`): an account comes from `google` (OpenID Connect, implicit flow in a popup, scope `openid` only: Google sends no e-mail, name or photo; the ID token's `sub` is stored as its HMAC under `AUTH_SUBJECT_KEY`, and the token's nonce is a one-time challenge; needs `GOOGLE_CLIENT_ID`, with `https://<site>/auth/google` as an authorized redirect URI of the OAuth client) or `noop` (tests: a pseudonym, no proof). An account is for the +1 and the reports; voting needs none (isoloir checks the passport itself). `zkpassport` proves a document for the polls (`accounts.ProveDocument`): the user scans the chip of their passport with the ZKPassport app; the phone proves in zero knowledge "adult, French" and gives an identifier unique per document for this domain and scope; nothing else leaves the phone. The proof carries a one-time server challenge (`auth_challenges`) and the voting key's commitment, bound as `custom_data`. `noop` also proves test documents (the pseudonym is the "document"). A Google account has no name: the user picks a pseudonym after the sign-in (`PATCH /api/auth/me`), shown to them alone; no route returns another user's pseudonym or +1. The API verifies the proofs itself with `zkverifier/verify.mjs` (Node, the open-source verifier of `@zkpassport/sdk`, `verifierMode: "local"`, no ZKPassport account or key); run `npm ci` in `zkverifier/` for a local zkpassport verification. Sessions are bearer tokens (hashed in `user_sessions`). Removing a provider from `AUTH_PROVIDERS` ends its sessions at once. The explainer for users is `/verification`, the privacy page `/about/privacy`. The Grafana dashboard **Users** follows the sign-ins, document checks, registrations, ballots, +1 and reports.

### The three parts of the site

The HTTP API is three products side by side, each a Go package with its own routes, its own storage and its own tables; `backend/api` only assembles them (middleware, CORS, rate limits, `/health`, `Wire`):
- **mirror** (`backend/mirror`, data in `backend/storage`): the politicians' record: persons, facts, stances, contradictions, the feed, shared links, score explanations, the users' +1 and reports.
- **accounts** (`backend/accounts`, providers in `backend/accounts/providers`): users, sessions, challenges (`app_users`, `user_sessions`, `auth_challenges`). It gives the others `RequireUser`, `UserFrom`, `ViewerID`, and `ProveDocument`, which checks a document's proof and gives its HMAC without keeping it.
- **polls** (`backend/polls`): the events of a vote, the presidential pre-poll first (`poll_events`, `poll_blackouts`, `poll_candidates`). The vote itself is isoloir, outside this repository. It keeps only ids: the candidates' cards come from the mirror through `polls.People`.

A part never reads or writes another's tables; a need crosses through an interface or a method. Tests of a part that needs signed-in users use `backend/accounts/accountstest` (an in-memory accounts store and the noop sign-in).

### Sharing, the feed, the score explanations and the users' reports

Every person, stance, contradiction and fact has a link: `/person/{slug}`, `?stance=`, `?c=` (contradiction), `?fact=` (`frontend/src/lib/share.ts`). A person's slug (`persons.slug`: `marine-le-pen`) is set by the database from the name at the insert (the trigger `persons_slug`; a second person with the same fold gets `-2`) and **never changes**, so a link outlives a rename; the API names a person by id or by slug in every `/api/person/{id}/...` route and `?person=`, and the site's links are by slug (`personPath`). A page by id (`/person/42`, the links shared before the slugs) is redirected (301) to the page by slug. The page opens the stance and highlights the item; the SvelteKit server reads the same parameters to fill the link preview (`routes/person/[slug]/+page.server.ts`, from `GET /api/share/{kind}/{id}`; it reaches the API at `API_INTERNAL_URL`, else `VITE_API_BASE`). Other pages set their preview with `meta` in their load data (the layout renders it). The share menu posts on X, copies the link, or opens the phone's share sheet.

`/feed` lists the contradictions and facts in an endless scroll (`GET /api/feed?sort=hot|new&kind=&person=&from=&to=&after=<cursor>`; a fact is listed once it is in a stance, a person only above the public floor of 4 facts). A signed-in user gives a +1 to an item (`PUT`/`DELETE /api/likes/{kind}/{id}`, table `feed_likes`, one per user and item), in the feed or on the person page (the contradiction groups and the facts of a stance carry the counts, with the viewer's marked; the link preview of a fact or a contradiction gives its count). A +1 goes on what the site asserts (a fact, a contradiction), never on what the politician asserts (a person, a stance): there it would be a vote. "À la une" (`sort=hot`, the page's default) ranks by the gravity of Hacker News, `(likes + 1) / (age in hours + 2)^1.8`, over the items of the last 14 days (all of them when one person is chosen), and stops after 200 items; "Récent" (`sort=new`) is newest first. The feed marks the viewer's +1 when the request carries their session. Every score has a "?" with its inputs and the formula (`GET /api/person/{id}/score`; a stance's from its card). `/about` explains the method, `/corrections` lists the corrections.

A contradiction is shown with its **explanation** (`contradictions.explanation`, `backend/contradiction/explain.go`, the prompt `promptrules.ExplanationPrompt`): a short text, often one sentence, that says only why the two facts conflict: the reader has the two facts with their dates right above it, so it never tells them again; it adds context only when the facts lack it (the question answered, which part of a quote the contradiction is about), written from the facts, the quotes of their sources and the passage of the article around them. The two facts carry the date of the statement when the fact has one (`facts.statement_date`, with its precision: the site prints "2013", not the day of the article that recalls it), else the date of their first source. The home page, the feed, the person page and the link preview show it (`whyOf`, `frontend/src/lib/i18n.ts`); until it is written the judge's one-line `reason` stands in. The maintenance pass writes the missing ones, 500 at most, the newest contradictions first, and writes one again when a fact of the pair changed or the prompt has a new `promptrules.ExplanationVersion` (`explanation_hash`). `./dm contradictions explain [--person "Name"] [--id 12,13] [--all] [--limit N] [--dry-run]` does it by hand and prints each text as a JSON line; a line with a `doubt` is a contradiction the source quotes weaken (someone else's words, a condition that did not occur): review it.

A signed-in user reports a fact or a contradiction once (`POST /api/reports`, `user_reports`, at most 20 waiting per user; `GET /api/me/reports`, shown on `/profile`). The worker judges the pending reports every minute beside its tasks (`complaints.JudgeReports`, the complaint judge with the target, its sources and the report), but the judge only advises (`ai_verdict`, `verdict_reason`): an LLM is easy to talk into agreeing, so nothing is removed until a person decides. The report waits in `review` (also after 3 failed judgings, without advice): `./dm complaints reports` lists them, `./dm complaints reports accept <id>... [--reason]` removes the target (a contradiction is dismissed so its pair is not detected again, a fact goes with its contradictions and the scores are recomputed) and enters the public log (`GET /api/corrections`, the judge's reason or `--reason`, never the reporter's words), `refuse <id>...` keeps it (`enabled`: true accepted, false refused, null undecided).

### The X bot
```bash
./dm bot draft --n 5                      # drafts of tweets, one per candidate: nothing saved or sent
./dm bot draft --contradiction 1206       # a draft of one contradiction
./dm bot draft --vote --n 6               # the next tweets on the vote (needs XBOT_VOTE_POLLS)
./dm bot accounts import backend/xbot/accounts.csv   # the starting list of accounts to tag (suggested)
./dm bot accounts list --status suggested
./dm bot accounts approve lemondefr decodeurs        # only approved accounts are tagged
./dm bot accounts add rnational_off --category party --person "rassemblement national" --status approved
```
The bot (`backend/xbot`, tables `x_accounts`, `x_posts`) tweets the contradictions of the candidates from an anonymous account. It picks one contradiction per candidate, the candidates it has not tweeted on for the longest first (the ones reviewed real first; never a reported one or one reviewed false); the LLM (`TaskTweet`, Opus 5.5 at effort high on claude-team) writes 2 versions in French (`xbot.Styles`): « Sobre » (factual, side by side) and « Satirique » (puns, mock headline, biting but never insulting), and a version whose « quote » is not in the quotes of the facts' sources, word for word, is dropped (`xbot.Check`; the fact's text is our summary and never counts as their words). The writer gets what the site shows: the explanation of the contradiction (else the judge's reason) and the date of each statement at its precision (« 2013 », never the day of the article that recalls it); the link is the person's page by slug. A tweet tags at most 3 accounts: the person (`twitter_handles`) and up to two approved outlets that published the facts (`x_accounts.domain`), the least recently tagged first; nobody else (no party, no fill-in: X counts unsolicited mentions as spam). A person approves every tweet before it is posted. When a source of one of the two facts is a tweet of the person's own account, the tweet is posted as a quote of it, the later fact first (`xbot.QuoteTarget`; never a tweet of someone else).

The bot also tweets about the vote, twice a day (`XBOT_VOTE_SLOTS`, default `09:00,16:00` Paris time, `none`: never; `backend/xbot/vote.go`): a call to vote taken in turn from `voteTexts` (no LLM; every line must stay true to `/election/[slug]/fonctionnement`), with the link of the event, approved on Telegram like the others. The event is the featured open one that `XBOT_VOTE_POLLS` names (`slug:id`, as `VITE_VOTE_POLLS`); the ballots counted come from the latest tally on the mirror (`XBOT_VOTE_MIRROR`, default the site's; read without checking its proofs). The count is told once the event shows its results (`min_voters`). The results themselves are not tweeted yet: the Telegram draft says when the vote passes `xbot.StandingsFrom` (300 counted ballots), the number from which they may be.

The worker runs the bot when `XBOT_ENABLED=true` (off by default). An hour before each slot (`XBOT_SLOTS`, default `12:00,19:00` Paris time) it drafts a tweet on a candidate with no open draft and sends it to Telegram (the alerts' bot, `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`, or `XBOT_TELEGRAM_BOT_TOKEN` and `XBOT_TELEGRAM_CHAT_ID`), read by long polling, only from that chat: the two facts with their quotes and sources, the reason, the versions. « Publier sobre » or « Publier satirique » approves that version (the contradiction is reviewed real, reviewer `xbot`), a reply to the message gives the text instead (the tags added when it has none), « Passer » skips it (the contradiction may come back), « Fausse contradiction » reviews it false and dismisses it from the site. An approved tweet goes out at its slot (at once when approved later); a draft undecided an hour after its slot expires, nothing posted. No tweet during the blackouts of the poll events (the electoral silence). Posting is the official X API (`X_API_KEY`, `X_API_SECRET`, `X_ACCESS_TOKEN`, `X_ACCESS_SECRET`: the app of the bot's account, read and write, OAuth 1.0a); without them the bot drafts and asks but posts nothing. `XBOT_LINK=true` adds the contradiction's link (X bills a post with a URL $0.20, $0.015 without).

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
6. **Score Update**: Recalculate honesty score: `exp(-0.6 * (B + 1.4 * ln(1 + E)) / (r * F + 3))`, F facts, B distinct contradictions (the pairs of one stance count once), E the extra pairs, r the candidates' contradictions per fact (`storage.ScoreRate`, the Rate of the baseline below): a person's contradictions against those expected of their facts. With few facts the count decides (one contradiction costs at most 18 points), with many the rate against the candidates' average (45 at the average, 70 at twice); no constant counts facts, so the scores hold when everyone's facts grow. A score depends on the candidates' rate: the maintenance pass ends by writing every stored score again (`backend/core/honesty.go`; the site explains it with the same numbers, `frontend/src/lib/score.ts`)
   A score is **reliable** (`core.ScoreReliability`) when the person is **covered**, their facts would hold 3 contradictions at the candidates' average rate (`core.NewScoreBaseline`, measured from the candidates' facts, distinct contradictions and scores, cached 5 min; about 3 300 facts on 2026-11-08, it moves with the rate), **or** when their score is strictly above the lowest score of the covered. The detection misses far more than it invents, so a score is far more often too low than too high: a score already above a covered one is rarely flattered by thin coverage (with so few facts the count of the contradictions put it there, and more facts bring more). A clean sheet needs coverage. The API gives `covered_facts` and `score_reliable` with every person; the site marks an unreliable score with 🚧 (« Score en cours de consolidation », the explanation on hover or tap and in the "?"), keeps it out of the podiums and the comparisons. The score itself does not change. The person page also shows the period the statements were collected over (`collected_from`, `collected_to`: the periods of the collection tasks that ended, else the dates of the facts). Beside the contradictions over time, it draws the facts per year of statement (`GET /api/person/{id}/coverage`, `CoverageChart.svelte`), so a long period with a thin start reads as what it is.
   The site never shows this number as is: it shows the **Tartuffe-Score**, 100 × (1 − honesty), as a barometer from « cohérent » to « tartuffe » (`frontend/src/lib/components/TartuffeScore.svelte`, no number in the headline, no marker under 10 facts). The API keeps `honesty_score`.
7. **Event Emission**: Insert events to `event_log` table for SSE streaming

### Key Components

- **`backend/storage/postgres.go`**: Core data layer - facts, persons, contradictions, affiliations, event logging. All DB operations go through here.

- **`backend/plugins/frpolitics/`**: French politics plugin - downloads vote data from Assemblée Nationale APIs, creates deputies/parties, generates facts from voting records. Entry point is `run.go`. A ballot on an article of a bill carries what the article does (`articles.go`): the text of the séance comes from the amendments voted in it, the article from the PDF of the text (`pdftotext`, poppler-utils in the image), the statement from the LLM, cached in `vote_articles`. `dm plugin frpolitics --mode rearticle [--rejudge]` rewrites the ballots stored before. A local votes run needs `pdftotext` on the PATH (poppler).

- **`backend/plugins/viepublique/`**: the speeches, interviews, press conferences and communiqués of the executive on vie-publique.fr (DILA open data, Etalab licence v2: the facts carry the attribution). The daily index (`vp_discours.json`, about 240 MB, cached 12 h in `VIEPUBLIQUE_CACHE_DIR`, else the temp dir) names the speakers and dates; the page holds the text. A task over a period takes the speeches of the person given or put online in it (most go online days later), never those where the person is listed as a journalist. Only the person's own turns are extracted, each after the question it answers. One document per speech and person; a window is logged (`viepublique_fetch_log`) once all its pages are stored, so a site outage leaves it open.

- **`backend/webfetch/`**: the way out to the sites we read. Each site owns how it is fetched: a `webfetch.Site` gets the shared `Client` and the `Request` (URL, source, purpose, person, period) and does what the site needs: a login, a cookie step, its own pace (`webfetch.Pace`: a rate, how many at once, halved on a 429 or 503 and climbing back), its own retries. A plugin registers the sites it knows (`webfetch.Register`, from `init`: vie-publique's bot-check redirect is in `plugins/viepublique/site.go`); any other host goes to the default site, a plain GET at one request a second and two at once per domain, with another browser tried on a 403. `Client.Do` adds the common headers of a desktop Chrome in French (never the name of the app) and records every request in `fetch_log` (source, domain, status, outcome, duration); a page outcome that is not a status (article ok, failed, too thin) is a `Note`. `webfetch.Redirect` asks where a redirect link points without following it (the Google result links, `google.com/goto`, resolved by the search client with the browser headers: a bare request from the worker gets a page of Google instead); a page that redirected and then refused the local fetch is read by the fallback extractor at the URL it was redirected to (`extractor.RedirectedError`).

- **`backend/contradiction/`**: LLM-based contradiction detection. Uses semantic similarity (pgvector) to find candidate facts, then LLM to judge contradiction.

- **`cmd/web.go`**: HTTP API with Chi router. Endpoints for persons, facts, contradictions, and SSE stream (`/api/events`).

- **`frontend/src/lib/realtime.ts`**: SSE client singleton with exponential backoff reconnection.

### Database

PostgreSQL with pgvector extension. Migrations in `supabase/migrations/` are idempotent (safe to re-run).

**Migration Strategy**:
- Migrations are run by **goose** on `dm web serve` startup (in prod) and applied locally by `supabase db reset` / `make db-start`.
- **Every new migration MUST begin with `-- +goose Up` and include a `-- +goose Down` block** — without them goose throws a parse error at boot and the deploy fails.
- Migrations should be incremental (ALTER TABLE, not DROP/CREATE).
- **Two migrations must never share a version** (goose panics at boot: `Migrations.Less`); before merging a branch, check its versions against main's. Versions older than the latest applied (a branch merged after main moved on) are applied: the runner passes `goose.WithAllowMissing()`.
- **Never reset the database without user permission** - data loss is permanent.

Key tables: `persons` (with `honesty_score`), `facts` (with `embedding` vector), `contradictions`, `person_links` (temporal affiliations), `event_log` (for SSE), `search_queue` (the queue of the news searches).

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
LLM_EXTRACTION_SHARE=0.6         # while the API key pays (every seat at its limit): the share of what is left of the
                                 # day that a round of extraction spends before the maintenance pass; what is left after
                                 # the pass starts the next round. 0 turns the split off. Real cost per phase: llm_api_spend_phase.
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

Production and local runs call one model name, `mirror-llm`, at `OPEN_AI_API` (production: the Cloudflare tunnel to the local GPU box). llama-swap on that box maps the name to the real model (its `llama-swap.yaml`); today it is Qwen3.8-27B (unsloth UD-Q3_K_XL GGUF, llama.cpp, fully in the 16 GB of VRAM). `FALLBACK_CHAT_MODEL` names the same model on OpenRouter.

- Every LLM decision call declares its task (`ChatRequest.Task`, `backend/llm/task.go`). One table there says which tasks think; the client then turns thinking on with a thinking budget (`llm.ThinkingBudgetTokens`) on top of the call's own `MaxTokens`. Thinking is off on the server by default. Today only the grouping gate (`TaskOccurrence`) thinks: a task thinks only when its golden shows a gain, and the nightly must still fit its window (a thinking call costs about 3 times a plain call).
- To measure a task with thinking: `make llm-goldens LLM_THINK=occurrence,detect` (the exact set of thinking tasks for the run, or `none`).
- To change the model:
  1. Serve the candidate on a free port (for example `llama-server ... --port 8095`).
  2. Measure it: `make llm-goldens LLM_API=http://127.0.0.1:8095/v1 LLM_MODEL=<name>`, then `make llm-eval` with the same values (grouping, about 2 h). Compare with the reference numbers in `backend/core/occurrence_eval_test.go`. Measure the thinking table again too: a new model can gain on other tasks.
  3. If it is better, edit the `mirror-llm` entry in `llama-swap.yaml` (llama-swap reloads the file). Nothing changes on the host, except `FALLBACK_CHAT_MODEL` if the OpenRouter name changes.
  4. Record the result in CHANGES.md.

## The anonymous vote (isoloir)

The vote is [isoloir](https://github.com/lanterne-labs/isoloir) (its own repository, beside this one; read its `SPEC.md` and README): passport eligibility through ZKPassport, ballots proved in the browser (Noir, UltraHonk), posted to Nostr relays, logged by the ledger's ingest in a signed append-only log (RFC 6962), mirrored to `lanterne-labs/vote-data`, decrypted as sums by the authority with proofs. isoloir knows nothing of tartuffe: keep it that way, a need of ours is a generic feature there.

- **Ledger** (`tartuffe.me/vote-log`): the host service `mirror-ingest`, built from the isoloir repository (`isoloir ingest --mirror-dir /app/mirror`), the log's only writer. The log key is `ISOLOIR_LOG_KEY` (its verifier key is the frontend's `VITE_VOTE_LOG_KEY`).
- **Authority**: the host service `mirror-authority` (`isoloir authority --db $DATABASE_URL`), with `ISOLOIR_AUTHORITY_KEYS`; no inbound port.
- **A poll**: `dm poll isoloir-setup` prints its JSON, `isoloir submit poll <file>` posts it. A change to a fixed field (keys, kind, registry, a new circuit variant) is a new poll; a later version of the same poll (`version + 1`, same id, submitted the same way) may change the title, the close (later only), future blackouts, and raise `publish.k_real` or `publish.min_gap` (never lower them; isoloir SPEC §13).
- Audit: `isoloir audit` (anyone, from the mirror).

### The vote app (frontend, isoloir)

The vote pages (`frontend/src/routes/election/[slug]/`, `VoteKeyDialog`,
`VotePassport`) run the isoloir protocol through the library
`@isoloir/web` (the repository `isoloir`, beside this one: read its
`js/README.md` and `SPEC.md`). `src/lib/vote/isoloir.ts` builds one
`Session` per event and gives its state as Svelte stores; the protocol is
the library's, never the app's. The library is not on npm: it is vendored
as `frontend/vendor/isoloir-web-<v>.tgz` (`frontend/scripts/vendor-isoloir.sh`
rebuilds it from the isoloir checkout, `ISOLOIR_SRC` to point elsewhere;
then `npm install`).

- Settings (build time): `VITE_VOTE_ORIGIN` (the ledger's origin, e.g.
  `tartuffe.me/isoloir`), `VITE_VOTE_LOG_KEY` (the log's verifier key,
  pinned), `VITE_VOTE_REGISTRY` (default 1), `VITE_VOTE_POLLS` (the poll of
  each event, `slug:id,…`), `VITE_VOTE_MIRROR` (the mirror as a directory
  URL) and/or `VITE_VOTE_MIRROR_REPO` (owner/repo, read by commit through
  jsDelivr). `VITE_VOTE_SIMULATE_PASSPORT=1` stands in for the ZKPassport
  app (a `dev` registry only), `VITE_ZKPASSPORT_DEV=1` the SDK's dev mode,
  `VITE_VOTE_APP_ORIGIN` (the site's build only) sends the vote pages of
  tartuffe.me to the app's origin.
- The candidates: the poll's options are names in slot order (blank last);
  their cards (person id, photo, honesty score) come from
  `GET /api/polls/<slug>`, matched by name. `npm run build:vote` fetches
  them into `static/vote/candidates/<slug>.json` (needs `VITE_API_BASE`),
  so the published app never calls the API for them.
- Local stack (from the isoloir checkout, `target/release/isoloir`; README
  "A local trial"): `isoloir relay`, `isoloir genesis`, `isoloir ingest
  --dev --no-stamps --mirror-dir <dir>`, `isoloir submit registry|poll`
  (a `dev` registry, a ranked poll), `isoloir authority --once` for a tally.
  Symlink the mirror into `frontend/static/mirror`, set
  `VITE_VOTE_MIRROR=http://localhost:<port>/mirror` and the other settings,
  and fetch the SRS once (`scripts/srs.sh`). `npm run build:vote` rewrites
  `.svelte-kit/generated` for the app's routes: restart the dev server
  after it.

## Deployment (the host)

Services on the host:

A push to `main` deploys `mirror`, `worker` and `frontend` through the GitHub workflow `deploy` (`.github/workflows/deploy.yml`: `host up --service <s> --detach` with the project token `DEPLOY_TOKEN`, a repository secret, then the status of the deployment until it ends: SUCCESS or SKIPPED, the service's watch paths unchanged, passes); `mirror-ingest` and `mirror-authority` are deployed the same way from isoloir's own workflow. The host cannot read the lanterne-labs repositories itself (its login is a personal GitHub account outside the org), so its own deploy on push does nothing; to deploy by hand, run `host up --service <s>` from the repository root (not `frontend/`: its root directory is a service setting).

- **`mirror`** — long-lived `dm web serve` (built from root `Dockerfile`). Hosts the HTTP API, the balance alerter, and exposes Prometheus metrics on `METRICS_PORT` (default 9090).
  Sign-in on `mirror`: `AUTH_PROVIDERS` (`google`; `noop` to test). The vote needs no account.
- **`worker`** — long-lived `dm worker run` (root `Dockerfile`, start command set in the service settings). Claims the collection tasks (`dm collect add`) and runs the ingest of their plugin for their person and period, as many tasks at once as the LLM endpoint serves (`llm.Concurrency()`: one on the local GPU box, on claude-team the processes of every seat: `CLAUDE_TEAM_PLAN_PROCS_PER_SEAT`, default 4, times the seats; every seat that is not at its limit serves at the same time); the facts of one person are inserted one at a time across the tasks. The daily feed and the maintenance pass in the order of priority (see Collection tasks above). Serves Prometheus metrics on `METRICS_PORT` (default 9090). Same env vars as `mirror` except no HTTP `PORT`. On a deploy the tasks it is on go back to pending and resumes where its fetch log stopped.
- **`mirror-ingest`** — the isoloir ledger (`isoloir ingest --mirror-dir /app/mirror --zkverifier /app/zkverifier/verify.mjs`), built from the `Dockerfile` of `lanterne-labs/isoloir`. Needs `DATABASE_URL`, `ISOLOIR_LOG_KEY`, `ISOLOIR_NOSTR_KEY`, `ISOLOIR_MIRROR_GIT` (`lanterne-labs/vote-data`), `ISOLOIR_MIRROR_SSH_KEY`, `ISOLOIR_ETH_RPC`. The only writer of the log: one instance.
- **`mirror-authority`** — the isoloir authority (`isoloir authority --db $DATABASE_URL --zkverifier /app/zkverifier/verify.mjs`), same image. Needs `DATABASE_URL`, `ISOLOIR_LOG_KEY`, `ISOLOIR_AUTHORITY_KEYS`, `ISOLOIR_MIRROR_GIT`.
- **`agora-ingest`**, **`agora-authority`** — the same pair for another election (Zcash agora, with a vote-data mirror of its own), with its own Postgres (`postgres-a31e66f7`) and keys. Not tartuffe's: nothing here reads or deploys them.
- **`observability`** — colocated Prometheus + Grafana under s6-overlay (built from `deploy/observability/Dockerfile`). Scrapes `mirror.host.internal:9090/metrics` and `worker.host.internal:9090/metrics` every 15s. A push does not deploy it: deploy it with `host up deploy/observability --path-as-root --service observability`. Grafana exposed publicly via the host-issued domain. One persistent volume mounted at `/data` (Prometheus TSDB at `/data/prometheus`, Grafana state at `/data/grafana`). Grafana alerts (`grafana/provisioning/alerting/worker.yml`: feed missing after 06:00 UTC, no maintenance pass ended for 12 h) go to Telegram: the service needs `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` before it is deployed.
- **`observability`** — colocated Prometheus + Grafana under s6-overlay (built from `deploy/observability/Dockerfile`). Scrapes `mirror.host.internal:9090/metrics` and `worker.host.internal:9090/metrics` every 15s. A push does not deploy it: deploy it with `host up deploy/observability --path-as-root --service observability`. Grafana exposed publicly via the host-issued domain. **App Sleeping must stay off on this service** (it was on until 2026-11-02: Prometheus and the Telegram alerts slept whenever nobody opened Grafana, and two thirds of the month went unscraped); the dashboard toggle is the only switch (`host.json` in the upload does not set it; `host config pull` + `apply` from a scratch folder does). One persistent volume mounted at `/data` (Prometheus TSDB at `/data/prometheus`, Grafana state at `/data/grafana`). Grafana alerts (`grafana/provisioning/alerting/worker.yml`: feed missing after 06:00 UTC, no maintenance pass ended for 12 h) go to Telegram: the service needs `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` before it is deployed.

- **Umami** (the audience measure) — its own host project, `analytics` (the service `umami`, image `ghcr.io/umami-software/umami`, and its own Postgres), so other projects can use it too: each site is a website of its dashboard (https://service.host.example). The site reaches it through itself (`frontend/src/lib/server/umami.ts`): `/stats/script.js` serves the tracker, `/stats/api/send` forwards the hits with the visitor's address in `x-umami-client-ip` (Umami's `CLIENT_IP_HEADER`; it keeps no IP). `frontend` needs `UMAMI_URL` and `UMAMI_WEBSITE_ID`; without them nothing is measured. The vote app measures nothing.

To collect facts, add tasks with `./dm collect add`; the worker runs them.

### Quality tracking
The Grafana dashboard **Quality** (`deploy/observability/grafana/dashboards/quality.json`, from the application DB) draws:
- the precision of the detection, from the verdicts in `contradiction_reviews`. To review, run `dm contradictions review sample --reviewer <name> --max 30 > todo.jsonl`, add `"verdict"` (real, debatable, false) and an optional `"note"` to each line, then run `dm contradictions review record --reviewer <name> < todo.jsonl`. A review keeps the facts as they were reviewed: it survives a reset or an audit, and it counts in the week the contradiction was detected;
- the quality gauges: stances, one-fact stances, claim groups, contradictions per score bundle. They are rows of `job_metrics` under the job `quality`, recorded at the end of each maintenance pass, or now with `dm quality record`;
- the verdicts of the periodic LLM audit.
- the result of each `/audit`: the rate of wrong rows per sample and per run, window and band apart (a graph and a table). Rows of `job_metrics` under the job `audit_run`, written by `.claude/skills/audit/scripts/audit_record.py`.

### Sources
The Grafana dashboard **Sources** (`sources.json`) shows, per source (news, viepublique, assemblee, photos, archive) and for the news per site, what the sites answered (ok, throttled, refused, gone, errors, timeouts), what the pages gave (articles ok, failed, empty or too thin, the bot check), the documents and facts per source, and the failures per hour. It reads `fetch_log` (90 days kept) and `source_documents`.

### Tasks
The Grafana dashboard **Tasks** (`tasks.json`) lists the collection tasks: the ones running, then the pending ones in the order the worker takes them (highest priority, then oldest; a task put back waits for its `not_before`), the pending tasks per operation and plugin, and the tasks that ended recently with their facts and errors.

The admin password of Grafana is the service variable `GF_SECURITY_ADMIN_PASSWORD`: the start script resets it at every start (`s6-rc.d/grafana/run`).

### Operations tracking
The Grafana dashboard **Operations** (`operations.json`) shows where each long operation stands: how far it is, its speed over the last 3 hours, when it should end at that speed, and what it produces (pairs judged, contradictions, claim groups, facts).
- The fold (`dm facts fold --apply` over everyone) and the detection over every person record their progress in persons, as `job_metrics` rows under the job `progress` (`storage.RecordProgress`). A new long operation without a table of its own records its progress the same way.
- The LLM usage of each feed and each maintenance pass: rows of `job_metrics` under the job `llm_usage` (target `endpoint/task/model`; metrics `calls`, `failed`, `input_tokens`, `cache_write_tokens`, `cache_read_tokens`, `output_tokens`, `thinking_tokens`, `cost_usd`, at list price). The worker also logs the cumulative report (`LLM usage of the process`).
- A collection batch is named at its creation: `dm collect add "Name" --from 2024-10-01 --operation "backfill 2y"`. Its progress is its tasks (`collection_tasks.operation`).

## Development Status

**Nothing is in production yet.** Don't worry about backward compatibility - if something breaks, fix it. This is pre-production development.

## Conventions

- **LLM output order: reasoning first, verdict last.** Every prompt that asks the model for a decision (same/different, keep/drop, yes/no, a label) must make the model write its reasoning BEFORE the decision field, and the decision must be the LAST field of the reply (JSON key order or line order). A model that writes the verdict first decides without reasoning and then justifies it. Parsers read fields by name, so the order costs nothing in code.
- **Every request to a site we read goes through `webfetch`** (`webfetch.Fetch`, or `webfetch.Shared().Do` for a stream): no `http.Client` of a plugin's own for a site, no User-Agent of our own. What a site needs (a login, a pace) is its `webfetch.Site`, in its plugin. The APIs we call (LLM, search, Twitter, Wikipedia, which asks robots to name themselves) are not sites and keep their clients.
- **Whether a judge votes is the endpoint's call.** A judge takes its verdict through `llm.MajorityVote(llm.Votes(task, n), ...)`: n samples on the local GPU box, whose verdicts flip between runs, one on a Claude endpoint.
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

This project uses **bd (beads)** for issue tracking, kept on this machine only (no Dolt remote, `.beads/` is not in git). Run `bd prime` to see full workflow context and commands.

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
