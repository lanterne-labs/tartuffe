---
name: audit
description: Full quality audit of Digital Mirror's production data, with the fixes. Reviews the new and debatable contradictions and re-reviews the oldest-reviewed ones, audits random samples of facts, votes, debates, stances and entities from both the latest window and a slice of history walked backwards run after run (so there is always something to audit), turns every judgement gap and data bug found into a prompt rule or code fix with a golden or a test, ships it (goldens on the Batches API, PR, merge, deploy), and prepares the data cleanup for the user's approval. Use it whenever the user types /audit, or asks to review the contradictions, check the latest facts, "make another pass", look for bugs in the data, or check what the worker produced since the last pass.
---

# /audit — review the data, fix what it shows, ship it

The project scores politicians on their contradictions. A wrong contradiction
is an accusation the site makes; a wrong fact or a wrong date is where most of
them come from. Each audit measures where the errors are now, removes the ones
in the data (with the user's approval), and changes the prompts and the code so
the same error does not come back. The fixes are part of every run: a review
without them repeats the same findings the next day.

Read CLAUDE.md first. The rules there hold (bd for issues, goose migrations,
`webfetch` for sites, reasoning before verdict in prompts, `llm.ForEach` for LLM
work, Fish shell quirks).

## Standing rules from the user

- **Authorized without asking**: writing to bd; recording contradiction
  reviews; the host commands (deploys, variable changes) except deletions;
  merging the PR the audit opens.
- **Always ask first**: any deletion of data (facts, contradictions dismissed,
  persons) and any reset. Prepare a script, show the counts, wait for the
  user's yes. Once they say yes, run it yourself.
- Never print a secret. Capture tokens and URLs in shell variables only, and
  filter output with `grep -v 'sk-ant'`. Seat tokens live in
  `CLAUDE_TEAM_PLAN_TOKENS` (named by account); the user's own account is
  never a seat.
- Contradiction definition (revised by the user on 2026-10-31, bd memory
  `contradiction-definition`): real at any gap are the person's words against
  their own ACTS (a vote, a decision, an alliance, a candidacy), a broken
  PROMISE or pledge, a stated-condition walk-back, a DENIAL of their own
  earlier position, a self-exempting double standard, and a claim against a
  court verdict. A change of OPINION alone is real only when the two
  opposite opinions are at most about three months apart and the person
  does not explain it; further apart, or explained, it is an evolution:
  `false`. Anybody may change their mind; the site must not call that a lie.
  `debatable` only when the facts truly leave it open. The rules the judges
  apply are in `backend/promptrules/{false_positives.txt,verify.go,contradiction.go}`.
- Debatable rows (decided with the user on 2026-11-01): a short reversal of a
  few weeks whose second statement is not plainly the opposite, and a
  self-exempting double standard, are KEPT; a pledge downgraded or replaced
  with no act, a general value against a specific measure, a prediction about
  someone else, a gap of many years, an explained learning, a doubtful fact
  and a likely vote error are DISMISSED. The site stays silent when unsure.
- The candidates (`dm persons candidate list`, bd memory `candidates`) are
  the only persons the pass judges; a contradiction of another person found
  before stays (the user: "we don't hide good contradictions").

## Run long work in the background

The user keeps talking to you during an audit. Start every command that can
take more than a minute or two in the background (`run_in_background`): the
goldens, the replay, dry runs and repairs against prod, the rescore, the
deploy watch. Answer the user
while they run, and report when each one ends.

## Setup

```bash
cd <the mirror checkout> && git checkout main && git pull --rebase
export DBURL="$(host variables --service postgres --kv | grep '^DATABASE_PUBLIC_URL=' | cut -d= -f2-)"
go build -o dm .
```

- On a new machine: the checkout needs its `CLAUDE.local.md` (never committed: it says what `host` is and gives the service ids; copy it from another machine), the host's CLI logged in, and `gh`, `go`, `uv`, `psql`. Nothing else of the audit lives on a machine.
- `dm` against prod: `env DATABASE_URL="$DBURL" ANTHROPIC_API_KEY= ./dm ...`.
  Never from a branch that adds a migration not yet deployed.
- Read-only SQL: `psql "$DBURL" -X -At -F ' | ' -c "..."`.
- Write SQL (approved cleanups only): `scripts/qw.py` of this skill.
- Work in the session's scratchpad directory, in a folder `audit-<date>/`.

## What one run looks at: the new window and a slice of history

Every run has something to audit. It always covers two things:

1. **The new window**: what the worker produced since the last audit
   (`.claude/skills/audit/scripts/audit_state.sh get last-run`; if there is none, the last 3 days). It shows
   whether the latest code works.
2. **A slice of history**: older data, walked backwards a step per run, so
   that every fact, stance and contradiction is audited in turn under the
   current rules. Most of the data was made by older prompts and code, so that
   is where most of the errors are.

The position in history is a cursor in the database (table `audit_state`,
so a run from any machine goes on where the last one stopped):
`.claude/skills/audit/scripts/audit_state.sh get cursor`, one
line such as `facts_below=152000 stances_below=21000`. If there is none, start
at the newest ids (`max(id)+1` of `facts` and `stance_documents`). Each run
takes the next band below the cursor:

| data | the slice of one run | cursor moves to |
|---|---|---|
| facts | ids from `facts_below - 8000` to `facts_below`; sample ~240 text facts and ~100 votes at random inside the band, ACTIVE facts only (`NOT EXISTS fact_relations duplicate` on the fact: the grouped duplicates are not errors and swamp the sample, 2026-11-01: 161/240) | the band's lower end |
| stances | ids from `stances_below - 1500` to `stances_below`; sample ~70 with 3–40 facts | the band's lower end |
| contradictions | the 40 rows whose LATEST review is the oldest (`reviewed_at` ascending), whatever their verdict, skipping those reviewed in the last 2 days | (no cursor: the order is by review age) |

When a cursor reaches the lowest id, it wraps to the newest ids and starts
again: the rules have changed since. Save the new cursor at the end of the
run (step 6). The new window and the history slice go through the same steps
below; the report gives their error rates side by side, which shows how much
better the current code does than the old one.

## 1. Measure

```bash
psql "$DBURL" -X -At -F ' | ' -v since="'<window start>'" -f .claude/skills/audit/scripts/measure.sql
```

It prints the contradictions and their last verdicts, the new facts per
source, the new entities, and the known failure gauges (Google redirect URLs,
tweets on a profile URL, undated news, sentinel dates, documents without text,
same-quote duplicates, fan-out, stance sizes, API spend). Compare with the last
audit's numbers (`.claude/skills/audit/scripts/audit_state.sh get last-run`): a gauge going up is a
regression to explain before anything else. Then read the worker's errors
and the X bot (the two sections below): they are part of every run.

### The worker's errors

```bash
.claude/skills/audit/scripts/worker_errors.sh <worker service id> <audit folder>/logs
```

It reads the warnings and errors of every deployment of the window, without
the seat-limit lines (`LLM unavailable`: every seat at its limit logs about
2 000 lines in 3 minutes, and `host logs -n 300` then shows nothing else),
and groups them by message. Read every group; for each one decide: expected
(the worker waits for a seat reset, a site answers 403 and the fallback reads
it), or a bug. A bug is fixed in step 4 with a test, like a data bug. Known
readings:

- `Rolled back fact after failure during ingestion` in the minutes a seat
  reaches its limit: the facts in flight; their documents are tried again.
- `claude request rejected … credit balance`: the API key is empty; the
  endpoint halts (expected since PR 75). Any OTHER rejected request is a bug.
- `Stripped trailing text from LLM JSON response` /
  `LLM corrected its JSON response`: the model wrote after its JSON. Read the
  `trailing_text`: when it holds a corrected answer the parser must keep the
  last one (fixed for the facts on 2026-11-12: a fact was lost with its
  withdrawn first answer).
- `Skipping fact: canonical_verb … outside the closed enum`, `Skipping invalid
  fact`: a fact lost; more than a few per day is a prompt or parser bug.
- `Failover extractor: all … extractors failed`: per site; compare with the
  dashboard **Sources**.
- `Embeddings request failed` beyond attempt 3: facts lost.
- An error that is none of these: read the lines around it in
  `logs/worker_errors.log` and find its cause before the samples.

### The X bot

The same script writes `logs/xbot.log`. Read it with the bot's table:

```sql
select id, kind, status, contradiction_id, slot, created_at, decided_at, posted_at, tweet_id, error, variants from x_posts where created_at >= :since order by id;
```

- every slot of the window (`XBOT_SLOTS`, `XBOT_VOTE_SLOTS`) has a row, or a
  reason: the silence, no candidate left, the LLM down until after the slot
  (`the draft is tried again every tick until its slot`);
- `reading Telegram failed … Conflict: terminated by other getUpdates
  request`: another process reads the same bot (a local `dm worker run` or
  `dm bot` with the production token, a second worker). The taps on the drafts
  are lost while it lasts: find the other reader and stop it. The bot sends a
  Telegram alert after 5 minutes of it (since 2026-11-12; on 2026-11-11 it
  lasted 4 h 30 and the day's draft expired undecided);
- a row `expired`: nobody decided. Say so in the report; it is the owner's
  call, not a bug, unless the Conflict above was running;
- a row with an `error`, or `approved` and not posted an hour after its slot:
  a bug (the X API answer is in `error`);
- read the variants of each draft as a tweet the site signs: a « quote » must
  be in the source quotes word for word, the dates at their precision, no
  insult, and the contradiction behind it must be one you would review real.
  A draft on a contradiction you find false is a finding of step 2: review it
  false, and it leaves the bot's picks.

### The extraction: did the pipeline work at all?

The last rows of `measure.sql` judge the extraction itself, before the
quality of what it produced. Read them first: a pipeline that failed makes
every other number of the run meaningless, and the quality steps would not
show it (the facts it did make are fine; the loss is what it did not make).
On 2026-11-09 the API's prepaid credit ran out at 00:20 UTC; the API answered
`400 … credit balance is too low`, which the worker took for an error of each
document: 18 000 documents (13 365 news, 4 709 tweets) were tried three times
and marked `failed` in three hours, 102 Twitter tasks ended "done" with 30
facts in all, no alert went out, and it was found the next day by chance.

Stop and fix before anything else when:

- **documents of the window by state**: a source with more `failed` than
  `extracted` documents, or a source with documents and no facts;
- **failed documents of the window by error**: an error that names the LLM
  endpoint, not the document (`LLM API`, `fallback claude-api`, `credit`,
  `unavailable`, `overloaded`, `rate limit`, a timeout) — an outage that
  consumed the documents' attempts. The endpoint must have halted instead
  (`llm.ErrUnavailable`: the plugins put the work back; `claude_batch.go` `do`
  and `callError` classify the API's answers, no Telegram message since 2026-11-14,
  the owner's choice: the logs say it). Fix the classification with a case in
  `TestClaudeAPIEndpoint_CreditErrorStopsTheRun`, then give the documents
  their attempts back (`dm documents retry --match <error text>`, dry run by
  default; `--apply` after the user's yes and once the cause is gone);
- **collection tasks of the window**: tasks `done` with items and no facts
  (`with_items_no_facts`), beyond a few: the task's `last_error` and
  `details` name the first cause;
- **facts per 100 documents**: a source whose yield fell by half against the
  14 days before, with no prompt change to explain it;
- **api spend by phase**: a day where extraction spent little while tasks
  were pending (the worker could not spend: an outage), or the maintenance
  phase absent for two days.

Every such finding is a code fix with a test in step 4 and a line in the
report, even when the data repaired itself since. Also compare the window's
facts per source with the tasks that ran (`collection_tasks`): a plugin that
ran and produced nothing is a bug until explained.

## 2. Review the contradictions

Take every contradiction with no review, every one whose last verdict is
`debatable` (a rule added since may settle it; the ones reviewed by `xbot` with the note "looks weak to the approver of the X bot" are the user's own doubts from Telegram: review them first and tell the user the verdict of each), and the history slice: the 40
rows whose latest review is the oldest, `real` ones included (a rule added
since may show one is false, and a fact behind it may have been wrongly
extracted). Dump them with their facts, dates and every source quote: (a detail missing from the quote may be in the article: read `source_documents.content` before calling a fact wrong, 2374)

```bash
env DATABASE_URL="$DBURL" ANTHROPIC_API_KEY= ./dm contradictions review sample --reviewer <model> --max 0 > sample.jsonl
# keep the ids that are unreviewed or debatable, then:
DATABASE_URL="$DBURL" uv run -q --with 'psycopg[binary]' python -I .claude/skills/audit/scripts/dump_pairs.py < todo.jsonl > enriched.jsonl
```

Review them yourself, in this session, one person at a time. **No subagents
or forks for the reviews or the samples**: the audit runs on this session's own
billing, never on another account's (the user, 2026-11-06, after five audit
agents hit a weekly limit). Write `contradictions.verdicts.jsonl` (`id`,
`verdict`, `note`; a false note names the cause: extraction error with the
fact id, rule the prompts lack, rule the judge missed, chronology, fan-out
duplicate), `contradictions.facts.jsonl` (`fact_id`, `problem`) and
`contradictions.rules.md` (judgement gaps the prompts do not cover, data bug
patterns).

Check every id has one verdict, then record:

```bash
env DATABASE_URL="$DBURL" ANTHROPIC_API_KEY= ./dm contradictions review record --reviewer claude-<model> < contradictions.verdicts.jsonl
```

## 3. Audit samples

After step 2, audit each sample yourself (no subagents), writing
`<sample>.audit.jsonl` and a short patterns note. Draw each sample from the
new window and from the history band, about half and half (all from the band
when the window is empty), and tag every row with where it came from:

| sample | size | what to judge |
|---|---|---|
| text facts (news, twitter, viepublique, debates) | ~240 random | against the quote: misattribution, gloss, wrong date, not a position, wrong entity, duplicate, garbled. Before calling a fact a gloss, read the article (`source_documents.content`): a plain report of what the person asks or did is not a gloss, even outside the quote (2374) |
| votes (`assemblee_law_page`) | ~100 recent | against the scrutin record: wrong object, inverted, garbled, unclear; read `backend/plugins/frpolitics/` for the cause |
| debates (`assemblee_debat`) | ~45 recent | speaker of the words, procedural talk, duplicates |
| stances | ~70 of the main persons, 3–40 facts | mixed subjects, strays, label, summary vs facts, duplicates, missed contradictions |
| new entities | all non-human since the window | political, advocacy (kept), state organ, company, foreign state, foreign party, junk |

Report each sample as an error rate by type. A type above about 3 % is a
pattern to fix in step 4; below, note it.

### One bad row means a search of the whole database

A bad fact or a false contradiction found in a sample or a review is one
instance of a pattern. Before step 4, look for the same pattern over every
row, not only the sample (the user, 2026-11-12: "anything you find not good
on a contradiction or a fact you fix it for the whole db"): write the query
that would have found the row, run it over the candidates (over everyone
when it is cheap), read what it returns, and put every row it confirms in the
cleanup of step 5. Keep the query when it will serve again
(`scripts/sweep_unnamed.sql`: the candidates' news facts whose article never
names them). Queries that paid off: an article that never names the person
(misattribution); a source on a social network read through the news search
(`instagram.com`, `facebook.com`: the page does not say whose post it is);
an agenda line stored as a position (« sera l'invité de », « a donné
rendez-vous sur »); HTML entities left in a quote (`&#xE9;`); a fact text
that dates a past event after its source (`published_at` of a live page is
the day the page was created).

## 4. Fix — the part that makes the next audit shorter

Branch: `git checkout -b fix/review-<MMDD>`.

For each **judgement gap** (a false row no current rule explains):
1. A rule in `false_positives.txt` under an existing `[bucket]` token (the
   auditor's enum is closed; no `%` in that file), and its short form in
   `contradiction.go` (the stance judge, which creates the edges, does not read
   the catalog).
2. A golden in `backend/contradiction/testdata/verify_cases.yaml` built from
   the real pair (`FP_<id>_<person>_<what>`, `expect: drop`, the dates).
3. A child issue of the epic `mirror-ds6` (keep the epic open).

For each **data bug** (a wrong date, URL, duplicate or entity from code):
find the root cause in the code, fix it there with a unit test, and write the
repair of the stored rows as a `dm` command with a dry run by default (examples
in the repo: `dm plugin twitter redate`, `dm facts resolve-redirects`).
Extraction rules go in `backend/promptrules/fact_extraction.go` with a case in
`backend/categorizer/openai_extractor_test.go`.

A finding that needs a design decision (the catch-all stances, the fan-out of
one fact) is filed in bd with the numbers and named in the report, not patched
in a rush.

Then the goldens, on the Batches API (15–40 min per batch; a cut run leaves
`batches/*.json` that the next run collects):

```bash
export ANTHROPIC_API_KEY="$(host variables --service worker --kv | grep '^ANTHROPIC_API_KEY=' | cut -d= -f2-)"
env OPEN_AI_KEY=unused OPEN_AI_API=http://127.0.0.1:1/v1 OPEN_AI_CHAT_MODEL=mirror-llm \
  go test ./backend/contradiction -run 'TestVerifyGolden|TestAuditGolden' -count=1 -v -parallel 1000 -timeout 180m \
  -args -llm-claude claude-sonnet-5-5 -llm-claude-models verify=claude-opus-5-5,audit=claude-opus-5-5 \
  -llm-claude-store <dir> -llm-claude-max-usd 30
# and TestAnalyzeGolden (./backend/core) and the extraction tests touched (./backend/categorizer)
```

Subtests sit under `TestVerifyGolden/cases/<name>`. Run them in the
background and keep working. A golden that fails on a defensible reading is
dropped with its rule rather than forced. `go vet ./...` and the unit tests of
the changed packages must pass (the storage time-zone test is a known failure).

Ship: commit (end the message with the attribution line the session gives),
push, `gh pr create` with the summary and the golden table, `gh pr merge
--merge --delete-branch`. The merge on main deploys `mirror` and `worker` by
itself: do not run `host up` or `host redeploy` on top (that built the
same commit twice and restarted the worker twice per merge, cancelling the
tasks in flight). Watch with `host deployment list --service worker` until
the merge commit is SUCCESS.

## 5. Cleanup — prepared, then the user's call

Write one script in the audit folder that does, in this order: delete the bad
facts (`printf 'y\n' | ./dm facts delete --ids ...`; their contradictions go
with them), dismiss the false rows (`./dm contradictions dismiss --ids ...`,
which also stops their re-detection), run the repairs with `--apply`, delete
the entities the user agreed are out of scope, then `./dm persons rescore`
(retry it: the public URL resets connections and deadlocks with the worker).
Show the counts and ask. On a yes, run it in the background and report what it
printed.

## 6. Report and close

The report, in this order, short:
1. The gauges of step 1 against the last audit, and the history band audited
   (ids and dates it covers).
2. Contradictions: real / debatable / false, and the causes of the false ones,
   new rows and re-reviewed old rows apart.
3. Each sample's error rate by type, new window and history band side by side,
   with two examples.
4. What was fixed (rules, code, goldens table), the PR and the deploy.
5. What waits for the user: the cleanup script, decisions to take.
6. The bd issues filed.

Record the run for the history (Grafana, dashboard **Quality**: error rate per
run, table of the runs). It reads the folder, so every `*.audit.jsonl` row must
carry `tag` (`window` or `band`) and `verdict` (`ok`, or the error type):

```bash
DATABASE_URL="$DBURL" uv run -q --with 'psycopg[binary]' python -I \
  .claude/skills/audit/scripts/audit_record.py --dir <audit folder> --at <UTC timestamp> --apply
```

Then save what the next run needs, in the database:

```bash
.claude/skills/audit/scripts/audit_state.sh set last-run "<UTC timestamp>: <the gauges in one line>"
.claude/skills/audit/scripts/audit_state.sh set cursor "facts_below=<N> stances_below=<M> (run of <date>)"
```

Then `git pull --rebase`, `git push`, `git status`.
