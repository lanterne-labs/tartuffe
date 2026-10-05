# CHANGES.md

This file tracks high-level changes to the codebase, lessons learned, and mistakes to avoid.

## 2026-09-15: Person honesty timeline — statement-time axis, replayed from live rows (mirror-3f9)

- New `GET /api/person/{id}/history` + "Évolution dans le temps" charts on the profile: weekly
  points with the all-time score, a trailing-12-month score, and facts per week. NOTHING is
  persisted: one SQL query dates every fact (earliest `fact_sources.published_at`, ingestion time
  as fallback) and every contradiction (the LATER of its two facts), and Go replays
  `ComputeHonestyScore` bucket by bucket (`backend/storage/person_history.go`). The last point
  therefore always equals `persons.own_score`, and evicted contradictions vanish from the past too —
  the timeline is the current record over time, not a log of what the pipeline once believed.
- SCORE FRESHNESS FIX surfaced by that invariant: `own_score` used to move only on contradiction
  events, so a person ingesting 100 clean facts kept a stale (lower) score until the next
  contradiction. Every path that changes the active fact set (mention insert, meta fold, vote
  fan-out) now goes through `refreshPersonScores` (`person_scores.go`): batch recompute of
  fact_count + own/members/aggregate from live rows, parents included, no SSE events.
  `RecomputeAllHonestyScores` also gained the duplicate-exclusion predicate it was missing.
- Meta facts carry a dateless `meta://` source: the history dates a fact by the earliest
  publication across its own sources AND its folded members' sources, otherwise every fold
  (and every contradiction repointed onto the meta) would jump to fold day.
- Time axis decision: STATEMENT date, not ingestion date. Ingestion only started 2026-05 while
  statements span years; on the ingestion axis the curve would just mirror the scraping schedule.
  Consequence: re-scraping old articles fills in the past instead of bumping today.
- Scoring-over-time decision: the headline formula is unchanged. More facts with no new
  contradiction raise the score only through the ratio term, bounded below by the absolute term
  (`exp(-0.03·bundles)`) — a contradiction is never erased by volume. "Is this person better
  lately?" is answered by the separate trailing-window series, not by decaying the record.
- Bad publication dates (1908, 2027) exist in prod: future dates clamp to now, anything older than
  10 years folds into the first point rather than stretching the x-axis.
- The replay (~30 ms on the largest prod person, dominated by two whole-table scans that grow with
  the corpus) is memoized per person in the web process. Invalidation cannot be event-based —
  the daily pipeline writes from another container — so every request revalidates against a
  cheap fingerprint (fact_count, own_score, contradiction count, current week) and a 24 h cap.
- Frontend gotcha: a Svelte `$state` named `history` silently shadowed `window.history` and the
  chart rendered the empty state with a 200 response in the network tab. Don't name state after
  browser globals.

## 2026-07-29: Meta-fact folding — cosine fix + LLM is the mandatory last decider

- Post-rollout prod audit (metas correct; Le Pen still had ~21 same-thing contradiction rows)
  exposed a UNITS BUG in the `dm facts fold` backfill: pgvector `<->` is L2 distance while the
  insertion thresholds are COSINE; applying the 0.70 floor in L2 space demanded cosine ≥0.955, so
  only near-verbatim pairs ever folded. Fixed (`cosineDistance` on the L2-normalized embeddings).
  Lesson: never mix pgvector operators and in-code thresholds without checking the metric — the
  same "0.70" differs wildly between spaces.
- Folding policy (user decision): the LLM position gate is now the MANDATORY last decider on every
  fold — embeddings only nominate candidates. The tier-1 "cosine ≥0.80 + compatible verb folds
  without the LLM" shortcut is removed from both the insertion path and the backfill (it survives
  only in legacy drop mode), because the stance-blind embedding_text puts same-topic-but-different
  claims above 0.80 too. Locked by TestFoldVetoedByJudge.
- IsSamePosition gained a PROCEDURAL-STAGES clause (réquisition vs verdict, indictment vs
  judgment, first instance vs appeal are different facts) after the corrected floor tried to fold
  the parquet's réquisition into the conviction meta; with it, that cluster and the
  different-numbers election cluster self-reject. Backfill meta-reuse also now synthesizes over
  existing member texts, not just the newly folded ones.
- Folding CONVERGES BY ITERATION: metas are active facts, so a second `dm facts fold` pass folds
  small metas into each other and absorbs stragglers; contradiction rows collapse further each
  pass as members repoint (Le Pen 38 → 34 after pass 1 at floor 0.75).

## 2026-07-28: Meta-facts — same-claim facts fold under one synthesized fact (mirror-gdb)

- Same-claim handling changed from DROP to FOLD. Before: a new fact ≥0.80-similar with a
  compatible verb (or LLM-judged "same claim" in the 0.70–0.80 gray zone) was silently discarded —
  its text/quote/source lost — while differently-phrased restatements slipped through and each
  paired independently in contradiction detection (Le Pen: ~20 near-identical
  innocence-vs-conviction contradiction rows). Now the fact is KEPT and linked as a member of a
  META-FACT: an LLM-synthesized aggregate (`facts.is_meta`, migration `20260728000000`) that alone
  participates in KNN pairing/contradiction checks. Members use the existing `fact_relations`
  'duplicate' link (general = meta), so every active-fact exclusion, ancestor redirect, and
  `RepointFactDuplicate` works unchanged; the meta reuses a representative member's embedding
  (same claim ⇒ same stance-blind slug — no embedder needed in the fold path).
- On each absorbed member the meta text is re-synthesized (`core.MetaSynthesizer`, temp 0) and the
  meta's `fact_pair_checks` are DELETED — text changed, so the detector re-judges it. Folded
  members skip stance analysis entirely (they join the meta's stance row) — one LLM pairing cost
  per meta, not per restatement. Wired in ingester (news), twitter, and `dm ingest files`;
  frpolitics votes intentionally not folded (structured, distinct events).
- SCORE CHANGE (user decision): the honesty-score fact count now excludes duplicate-marked facts
  (matches the already-deduped `persons.fact_count`) — repeating a claim N times weighs once.
- Backfill: `dm facts fold [--person] [--apply]` clusters existing same-claim facts (union-find
  over embedding candidates + the same verb/LLM gates), builds metas, repoints + collapses
  duplicate contradiction rows, recomputes scores. DRY-RUN by default.

## 2026-07-26: Deterministic eviction gate — temp 0 + verdict pinning + order-swap agreement

- The auto-evict decision is now built to be a DETERMINISTIC function of the pair, closing the
  "survived four audits, died on the fifth" ratchet (mirror-bii):
  (1) auditor LLM calls run at temperature 0.0 (was 0.1 — the only sampling-noise judge left in
  the eviction path; verifier already 0.0);
  (2) VERDICT PINNING: a KEEP verdict stores `contradictions.last_audit_hash` =
  `contradiction.AuditInputHash(...)` — sha256 over the full audit prompt/rulebook AND the row's
  facts/dates/reason. The audit step skips rows whose hash matches: an unchanged pair is never
  re-judged, so its verdict cannot flip on server noise. Editing the prompt or repointing a fact
  changes the hash and re-opens the row (migration `20260726000000`);
  (3) the confirm pass is now ORDER-SWAPPED (statement B first) and must reach remove citing the
  SAME rule token — a verdict that flips under order or reproduces "remove" with a different
  justification (the Vuibert signature) is confabulation and keeps the row as unstable.
- Auditor prompt gained a direction-mapping instruction under POSITION REVERSAL (abolish-the-age
  vs harden-the-reform are OPPOSITE directions, not same_direction) — the exact Bardella misread.
- Lesson: with a stochastic judge and one-way deletions, "2-of-2" via an identical re-roll is not
  a second opinion — it is the same dice. Independence must come from a deterministic prompt
  transformation (order swap) + reasoning agreement (same rule), and idempotence from pinning.

## 2026-07-26: Why the auditor deletes real contradictions (mirror-bii) + restores

- Restored two wrongly-evicted real contradictions: Bardella retirement flip (cid 487, via clearing
  the suppressing negative pair-checks and re-running real detection) and Vuibert non-candidacy
  (cid 488, via `tools/restore_contradiction` after the CURRENT detector said NO 3/3 on a pair the
  June detector caught — the detector regressed on it too, it was never detector-locked).
- Root cause of prod-vs-golden divergence, established by live repro + A/B: (1) hard semantic bug —
  the auditor reads "supprimer tout âge légal" as SAME direction as "durcir la réforme" and evicts
  via same_direction/different_subjects (new golden TP_487 locks this RED; TP_114 same family);
  (2) the auditor verdict is STOCHASTIC on borderline keeps, and 5-day re-audits with a PERMANENT
  negative pair-check form a ratchet — ~33%/cycle delete probability kills any borderline-real row
  within weeks even though goldens mostly pass (Vuibert: survived 4 audits, died on the 5th);
  (3) goldens are single-sample and OPEN_AI_KEY-gated out of CI, so the suite sat red (TP_114,
  TP_103, FP_161) with nobody seeing it. Lesson: a green golden on a stochastic judge does NOT
  imply prod safety — the prod loop samples the judge repeatedly and deletions are one-way; any
  auto-destructive LLM verdict needs an idempotence/stickiness guard, not just better prompts.
- Eviction forensics recipe: deleted contradictions leave `contradiction_added` events (IDs) and
  their FR reason survives in `translations` (no FK cascade) — join those to reconstruct what the
  auditor deleted.

## 2026-07-25: Repeatable "forensic" workflow — why does person X have no contradictions?

- New read-only audit tooling: `scripts/forensic.sql` (per-person corpus/time coverage, detection
  coverage vs the detector's own KNN criterion, stance fragmentation, eviction trail via
  `contradiction_added` events which outlive deleted rows, queue membership, pipeline/audit health)
  + `scripts/forensic_pairs.sql` (dumps closest judged-NO pairs and pairs just outside the KNN
  threshold for second-judge review). Both take a `-v targets="regex"` person filter and run via
  `host run --service postgres`. Orchestrated by `.claude/workflows/forensic.js` (invoke the
  "forensic" workflow; writes `scoped_reports/forensic_report.md`).
- Key method insight: surviving `contradictions` rows understate detection — auditor evictions flip
  `fact_pair_checks` to false and delete the row, so the only durable record of "the pipeline DID
  find something" is the `contradiction_added` event log. First run: ~2/3 of all contradictions ever
  created have been evicted (e.g. Bardella 9→1, Macron 6→1, Le Pen 97→39).

## 2026-06-27: Translation subsystem — generic store + async final pipeline phase (mirror-rgw, phases 1-2)

- New extensible translation layer. `translations` table (entity_type, entity_id, field, lang, text,
  source_hash) holds translations for ANY language — adding a language is config, not schema. New
  `backend/translate` package: `Translator` interface + OpenAI impl (any language pair, auto-detects
  the source when lang is unknown), `DefaultTargetLangs{en,fr}`, hash-based staleness.
- New `dm translate run` + a FINAL step of `dm pipeline daily` (Step 8): scans facts, stances
  (label+summary) and contradictions (reason), fills missing/stale translations. Decoupled from
  generation — runs LAST, best-effort, never blocks the pipeline.
- Generation is now DECOUPLED from translation: fact extraction, stance refresh, and both
  contradiction-reason paths no longer emit inline `_en` (extraction writes only the native
  `normalized_fact`; stance label/summary + contradiction reason are pinned to French). All `_en`
  now comes from the async translate phase, which bridges English back into the legacy `_en` columns
  so the API/frontend keep working. The former mid-pipeline EN-only fact repair (Step 3b) is removed
  as redundant. Read-path migration to the generic table (N languages) is the remaining phase.

## 2026-06-27: FP catalog — partial-vs-total + qualified-general-vs-specific buckets (mirror-me2)

- The shared false-positive catalog (`backend/promptrules/false_positives.txt`, used by detector +
  auditor + verifier) gained clauses for FP shapes the 2026-06 audit surfaced: a targeted/PARTIAL
  measure vs declining the TOTAL/universal version (`[procedural_scope]`); a QUALIFIED general stance
  ("oppose X en permanence") vs endorsing ONE specific case (`[vague_vs_specific]`); plus softer clauses
  for conditional-not-triggered (`[condition_later_met]`) and complementary same-day facets
  (`[complementary_facts]`). Reused existing rule tokens, so the auditor's closed enum is unchanged.
- `verify_cases.yaml` gained 2 reliably-passing golden cases (FP_177 partial-vs-total, FP_245
  general-vs-specific). The conditional-not-triggered / complementary-facets / different-metrics shapes
  were trialled but NOT asserted — the verifier LLM (temp 0.1) does not reliably drop them and the
  golden test is inherently flaky at the margin. Lesson: an LLM golden test can only assert shapes the
  judge handles RELIABLY; borderline ones belong in the issue as a documented gap, not as flaky asserts.

## 2026-06-27: Extraction grounding rule — no over-specification (mirror-k1q)

- The fact extractor was injecting specifics ABSENT from the cited quote: a named body
  ("GIEC" for a generic "les scientifiques"), fabricated dates/figures, invented places
  ("le Salvador"), motives, or an outlet ("Politico") — sometimes grafted from a DIFFERENT
  statement/source in the same article. Rule 6 ("be specific") had no counterweight.
- `CoreFactExtractionPrompt` now carries a GROUNDING rule (in the original_quote field spec and
  the Self-Consistency Check): every specific in normalized_fact must appear in original_quote;
  keep the fact as generic as the quote is; never import a detail from a different sentence/source.
- Live golden test `TestRemoteLLMExtractor_NoOverSpecification` (categorizer) reproduces the
  GIEC/Salvador/Politico traps and asserts none leak into a fact. Local-only (OPEN_AI_KEY-gated).

## 2026-06-24: Contradiction recording rejects cross-person pairs (mirror-ez8)

- The detector's KNN candidate search wasn't strictly person-scoped, so it occasionally paired a fact
  about person X with one about person Y and filed it under one of them — a guaranteed "different
  subjects" false positive (e.g. a Patricia Lemoine fact vs a Darmanin fact). 3 of 74 prod rows were
  this. NB the `fact_mentions` were correct; the bug was in the PAIRING, not the linkage.
- `RecordContradiction` now inserts only when the two facts share a mentioned person (via
  fact_sources -> fact_mentions); a no-shared-person pair is skipped with a WARN (RETURNING is empty
  -> pgx.ErrNoRows handled as a skip). Deterministic guard at the single insert chokepoint;
  same-person and stance-path contradictions are unaffected.
- New `dm contradictions prune-cross-person [--apply]` lists/dismisses pre-guard rows (FP path:
  removes + suppresses re-detection + recomputes score). Cleaned 225/230/234 in prod.
- Lesson: this whole FP subclass is DETERMINISTIC (pure SQL: "do the two facts share a person?") — a
  SQL scan even caught one row (234) the LLM audit missed. Kill deterministic FP classes with a guard,
  not the LLM verifier (mirror-me2).

## 2026-06-24: Ingestion rejects empty-quote web facts (mirror-k1q)

- A prod audit (audit-prod-data workflow) found `news`/`twitter` facts stored with an EMPTY
  `original_quote` — unverifiable hallucinations with no snippet behind them. Nothing checked the
  quote before; `ParseFactJSON`/`insertSingleFact` validated only `SubjectEntity`.
- `insertSingleFact` now skips a web-extracted fact whose quote is blank (new
  `backend/core/quote_guard.go`: `quoteMissing`), as a per-fact skip-with-WARN that does NOT abort the
  batch (mirrors the entity-rejected skip). Scoped to `news`/`twitter`; structured votes
  (`assemblee_law_page`) always carry a quote from law metadata and are untouched.
- Rule: a web/news fact with no verbatim quote is a hallucination — never store it. (The
  extraction-prompt over-specification half is handled by the 2026-06-27 grounding-rule entry above.)

## 2026-06-15: Stance merge for fragmented persons — pairwise gate + direct fold (mirror-86l)

- `dm stances merge` (and the daily pipeline's stance-merge step) had a hard recall failure on the
  most-fragmented people. Two regimes by stance count: <= `allStancesLLMCap` (8) hands ALL stances to
  one `MergeClusterDecision` (good recall at small N, unchanged); above the cap the single all-stances
  call returns ZERO merges (verified: Ciotti's 30 stances incl. 5 "sécurité publique…" + 3 Olympics →
  0 merges). Replaced the over-cap path with PAIRWISE: each embedding-similar pair (>= recall floor)
  is a focused 2-stance yes/no to the LLM, confirmed pairs union-find into groups.
- Two non-obvious fixes after the first cut still missed real dups: (1) a loose 0.65 candidate floor
  let union-find chain a 5-stance supercluster through a fuzzy "identité/société" bridge — the LLM
  pair-gate is NOT reliable in the 0.65-0.80 band (it confirmed immigration↔identité at 0.68). Raised
  the floor to **0.82**, ABOVE the empirical noise band: genuine near-dups embed at ~0.86-0.90, noise
  pairs stay <=0.80. (2) The confirmed group was then re-litigated with a SECOND `MergeClusterDecision`
  over the whole group, which reintroduced the same recall collapse (merged nothing). Now a
  pre-confirmed group is folded DIRECTLY via `applyMergeSubgroup` (which regenerates the unified
  label/summary from the merged facts) — the pair-gate IS the decision.
- Removed the dead centroid-clustering Phase A (agglomerative average-linkage + sub-clustering, the
  `MaxCluster`/`SubclusterSim` knobs): centroid distance is a poor "same subject" proxy (paired
  "sécurité publique" with "relations publiques"). Net effect on prod: far fewer LLM calls per person
  (Zemmour 18 → 1) and the genuine dups actually merge. Merging also surfaces previously-hidden
  contradictions (the within-stance exhaustive compare runs over the newly co-located facts), which
  is the mirror-cfy class (commit-to-not-X vs do-X split across stances).
- Lesson: with a per-pair LLM gate, the gate's confirmation must BE the merge. Re-asking the LLM over
  the union-find supercluster throws away the focused decision and brings back the recall problem it
  was built to avoid. And don't trust a fuzzy LLM gate to compensate for a too-loose similarity floor.

## 2026-06-14: Person name canonicalization extended beyond accents

- `util.NormalizePersonName` now folds ALL surface variants to one canonical form: accents stripped, typographic punctuation → ASCII (curly apostrophes ’‘ʼ′ → ', curly quotes → ", en/em/figure dashes → -), and internal whitespace collapsed. Triggered by a daily-run finding: "conseil … de nice-côte d’azur" stored with a curly apostrophe that `immutable_unaccent` folds but the accent-only normalizer didn't — the same variant-splits-a-person risk as accents, e.g. "Valéry Giscard d’Estaing" vs "...d'Estaing".
- The dedup detector + heal are now Go-based on `NormalizePersonName` (replacing the `immutable_unaccent`-SQL `GetAccentDuplicateGroups`/`StripAllPersonAccents` with `GetAllDedupMembers` + `RenamePerson`), so the dedup key is IDENTICAL to the form names are stored under — catching dash/whitespace variants that immutable_unaccent misses. `dm persons unaccent` groups in Go and rewrites every name to the canonical form. Lesson: the in-code normalizer and the dedup key must be the same function, or they drift (immutable_unaccent and RemoveAccents disagreed on curly apostrophes).

## 2026-06-13: Person names are stored accent-free (kills accent-duplicate rows)

- Root fix: person names are now normalized to an ACCENT-FREE canonical form on every create/match path via new `util.NormalizePersonName` (= lowercase + trim + RemoveAccents). Wired into `FindOrInsertPersonWithType`, `findOrInsertPersons`, `AddPerson`/`AddPersonWithType`, `GetPersonIDByName` (storage), `dm persons add`, and crucially `frpolitics` `UpsertParty`/`UpsertDeputy` — which INSERT directly with `ON CONFLICT (name)` (exact) and were a live source of dups (curated "éric ciotti" vs news "eric ciotti"). `NormalizeString` stays accent-preserving for non-person uses. Lesson: accent-insensitive MATCHING (immutable_unaccent in SELECTs, added 2026-06-09) wasn't enough — the frpolitics direct-insert path with exact ON CONFLICT could still split a person; the only robust invariant is storing the canonical form.
- One-time heal: `dm persons unaccent [--apply]` (dry-run by default) finds rows colliding under `lower(immutable_unaccent(name))`, merges each group into the curated/most-facts survivor (`ChooseCanonical` + `MergePersons`), then `StripAllPersonAccents` rewrites every name accent-free. A prod run found 55 dup groups with major politicians split across two rows (éric zemmour 82 facts split 42/40, édouard philippe 36+, élisabeth borne, laurent nuñez…). Note: stripping existing names is irreversible (é→e); accepted to make the invariant uniform. Order matters — deploy the accent-free code before/with the heal so the nightly frpolitics doesn't re-create dups against freshly-stripped names.

## 2026-06-12: Auto-evict requires a cross-judge (auditor + verifier) to agree

- `applyAuditRemoval` now deletes a contradiction only when THREE signals agree: the auditor's first pass = remove, a confirming second auditor pass = remove (variance guard), AND the VERIFIER (refute pass) also drops it. The verifier is a different, reversal-aware judge (it has explicit "this IS a reversal, keep it" patterns and keeps Vuibert/Martin/Retailleau/denounce-then-ally in its golden), so it VETOES deletion of a genuine contradiction the auditor mislabeled into a fuzzy bucket (different_subjects, announcement_then_action, …). This is the durable fix for the over-removal the prompt tweak alone couldn't close (the local model still over-removes TP_103 2-of-2).
- Chosen over a "bucket gate" (never auto-delete on the 6 fuzzy buckets), which was crude — it discards the auditor's per-case judgment on whole buckets. Cross-judge uses every case's full reasoning and just requires a second independent opinion to concur before deleting a prod row. The verifier is built only under `--apply` (one extra call per flagged row; free local LLM).

## 2026-06-12: Auditor reversal-protection + AUDITOR golden test

- The contradiction AUDITOR (behind `--apply` / the nightly auto-evict) was over-removing real reversals: a 2026-06-12 prod `--apply` run deleted genuine flip-flops (Vuibert "won't be a candidate" then led a winning list; Retailleau pleads-for vs against a primary; Martin announced-run then announced-not-run) by mis-filing them under the fuzzy buckets (announcement_then_action, third_party_narrative, different_events, vague_vs_specific). Root cause: the auditor prompt had NO positive "these ARE contradictions, keep them" section — only "be conservative, remove FPs" — whereas the verifier has explicit YES patterns. Added a "**NEVER REMOVE A GENUINE CONTRADICTION**" block to `auditPromptTpl` (position reversal / commitment-vs-action / denial-vs-finding / denounce-then-ally; reported speech = the person's own claim, not third_party_narrative). Auditor keep-rate on the golden went 5/8 → 6-7/8.
- New `TestAuditGolden` (`audit_test.go`) runs the AUDITOR over the same `verify_cases.yaml` and asserts keep-cases are NOT removed and drop-cases ARE removed — the verifier and auditor are different prompts and diverge, so the auditor needs its own golden. Added 3 prod reversal cases (TP_100 Vuibert, TP_111 Martin, TP_152 Retailleau) as KEEP.
- Residual / lesson: the local model still over-removes `TP_103` (denounce-then-ally → different_subjects) 2-of-2 despite the exception in both the catalog and the auditor prompt — a hard model limit prompt-tuning can't close. Under auto-evict this means consistently-misclassified reversals can still be deleted; the durable safety net is a bucket gate (never auto-delete on the fuzzy reversal-dismissing rules), kept as a follow-up.

## 2026-06-12: Nightly contradiction audit now AUTO-EVICTS false positives

- `runContradictionAuditIfDue` (the daily-pipeline audit step) now passes `Apply: true`. Every audit cycle, a verdict=remove confirmed by a 2nd independent auditor pass (2-of-2) is deleted via `DismissContradictionAsFalsePositive` (negative pair-check + honesty-score recompute) — false positives no longer accumulate between runs. Reverses the earlier "advisory-only / manual --apply" decision (2026-06-09) at the user's request. The Telegram summary now reports "N checked, N evicted, N kept (unstable)" and two new job-metrics (`deleted`, `unstable`) are recorded for Grafana. Guardrail unchanged: only 2-of-2 removes are deleted; a disagreeing confirm pass keeps the row. Note the auditor occasionally over-removes a borderline-real contradiction — accepted trade-off for a self-cleaning pipeline.


## 2026-06-11: Golden FPs from the daily run — double-negative + homonym

- Added 3 prod-sourced cases to `verify_cases.yaml`. FP_166 (Darmanin "immense échec" vs "no more resources" — failure≠budget) already evicted by existing buckets. FP_170 (Bergé pro-imprescriptibilité in BOTH facts; the flag misread the double negative — the statute of limitations, not imprescriptibility, is what "protects perpetrators") now evicts after a `paraphrase_same_position` clause: "map each statement to the position it IMPLIES; opposing a measure = supporting its opposite". Golden 22/22 stable.
- FP_169 (Attal homonym "primaire": école primaire vs élection primaire) is NOT asserted — the local model cannot be made to evict it via the verifier even with a guard naming the exact misread (a wrong claimed_reason anchors it). Documented as a deferred skip; the real fix is stance/KNN-level separation (tracked in mirror-ezt). A short homonym note stays under `different_subjects` for clearer cases. Lesson: the verifier weighs the claimed_reason heavily, so a reason that asserts a false link can defeat a correct catalog rule on a weak model — some FP classes must be prevented upstream (don't form the pair), not refuted downstream.

## 2026-06-09: Detector — claim-vs-official-record gate fix + example-block trim

- The HARD GATE now states that an ATTRIBUTION ("X announced/stated/declared/claimed/promised Y") IS the person's own utterance (not third-party narration), and broadens "authoritative finding" to include an audit, official report, or verified official record. This fixes the detector silently NO-ing a real contradiction where a person's announcement is refuted by an official record (e.g. "CEO announced record profits" vs "the official quarterly report showed a loss") — the local model had been bucketing both as "récits de tiers" and failing the gate.
- To fit that addition under the 12k context ceiling (see the catalog note below — the detector prompt has almost no headroom), 4 redundant examples were removed from `ContradictionRulesAndExamples`: PAIR 13/14 (covered by PAIR 15 + `conviction_vs_acknowledgment`), PAIR 20 (dup of 17), PAIR 24 (dup of 16). 32 → 28 examples. Lesson: examples are load-bearing — an earlier pass also removed the temporal ones (PAIR 1 learning-over-time, PAIR 5 different-events) and that immediately regressed "unknown-date skepticism" (the detector flagged a for/against vote with one missing date). They were restored; only same-bucket duplicates are safe to drop. Validate example edits with the detector live test, and the authoritative check is `dm eval run`.

## 2026-06-09: Single shared false-positive catalog across detector, auditor, verifier

- The FP taxonomy was duplicated and DIVERGED across two constants: `RefuteContradictionPrompt` (insertion/reverify verifier — had numbers/forecast/endorsement buckets the detector lacked) and `ContradictionRulesAndExamples` (detector + auditor). The two gates therefore disagreed on most pairs (the verifier approved at insertion what the auditor rejected 5 days later). Now there is ONE source: `backend/promptrules/false_positives.txt` (`//go:embed` → `promptrules.FalsePositivePatterns`), the deduplicated union of both lists, injected via `%s` into the verifier prompt AND `ContradictionRulesAndExamples` (so detector + auditor get it too). `contradiction` now imports `promptrules` (dependency-free, no cycle). Each bucket is tagged `[token]`; the auditor's closed enum gained 6 tokens (numbers_measurements, forecast_vs_current, procedural_scope, condition_later_met, same_direction, endorsement_vs_non_candidacy) and now GENERATES its prompt's token list from the single-source slice. `TestCatalogEnumInSync` locks catalog↔enum; `TestCatalogSprintfSafe` forbids literal `%` in the catalog (it is concatenated into fmt.Sprintf templates). Net: verifier golden 19/20 → 20/20, and the auditor's wrong `different_subjects` removal of the maximi denounce-then-ally case (a real contradiction) is fixed by the same edit.
- HARD constraint: the local GPU model (also prod's, via lite-llm) has a 12k context and the detector reserves 4096 output tokens, so the detector prompt has only ~tens of tokens of headroom. The shared catalog MUST stay ≤ the inline NO-list it replaced (~4900 bytes) or every detector call 400s with `maximum context length exceeded`. When editing `false_positives.txt`, keep it terse and re-run the detector live test — bytes are budgeted. Reducing the detector's output reservation to make room instead truncated Gemma's chain-of-thought ("no response parsed"), so size the INPUT, not the output.
- Prompt-tuning lesson (local Gemma): do not NAME a false-positive hook the model can latch onto. An early `third_party_narrative` wording said "REPORTED SPEECH ... IS the person's own utterance" — the model seized "reported speech" and inverted it to drop a real attitude reversal. Reframing positively ("a statement of what the person said/declared/felt COUNTS as their own utterance — NOT this bucket") fixed it. Validate prompt edits against `TestVerifyGolden` 3× (the model flaps run-to-run).

## 2026-06-09: Contradiction auditor can now DELETE false positives (`check --apply`)

- The contradiction auditor was advisory-only: a prod audit flagged ~79% of recorded contradictions as false positives (verdict=remove) but `executeContradictionAudit` only logged/counted them, so the FPs stayed and depressed honesty scores. New `dm contradictions check --apply` acts on the verdict: a `remove` triggers a confirming SECOND auditor pass and, on a 2-of-2 remove, the contradiction is deleted via the new `storage.DismissContradictionAsFalsePositive`. Read-only by default (mirrors `reverify --apply`). The daily-pipeline audit pass stays advisory (decided manual-only) — wire `Apply:true` into `runContradictionAuditIfDue` if we later want it self-healing.
- Key design: `DismissContradictionAsFalsePositive` records a NEGATIVE `fact_pair_checks` row (is_contradiction=false) instead of clearing it like `DeleteContradiction` does. Clearing would let the detector re-raise the pair next run, the lenient insertion verifier re-approve it, and the next audit re-delete it — thrashing every cycle. The negative pair check makes the auditor's "not a contradiction" verdict stick. Both methods now share a private `removeContradiction(id, suppressReDetection)` body. Tradeoff: a wrongly-suppressed real contradiction won't self-correct (the 2-of-2 conservative auditor is the guard).
- Lesson: the insertion verifier (refute prompt) and the 5-day auditor disagree on the bulk of pairs — the verifier approved both contradictions created the same day the auditor rejected them. `reverify --apply` uses the verifier, so it can't clean up what the verifier itself let through; the backlog cleanup must use the auditor's stricter judgment.

## 2026-06-09: Person name matching is accent-insensitive everywhere (fixes éric/eric dup)

- `findPersonIDByName` (the fast-path lookup behind `AddPerson`/`AddPersonWithType`) used exact `WHERE name = $1`, so a news-extracted "eric ciotti" never matched the curated "éric ciotti" and inserted a near-duplicate person (split facts/contradictions/score across two rows). Now matches `lower(immutable_unaccent(name))` like every other find-or-insert path. The `ON CONFLICT (name)` insert guard only catches exact-name collisions, so the unaccent match in the lookup is what actually prevents the dup.

## 2026-06-08: N-ary contradiction groups — collapse the pairwise wall (gh#21)

- **`contradictions` gets an N-ary GROUP layer without changing its pairwise core.** Migration `20260608010000` adds a nullable `base_fact_id` pointer (+ denormalized `group_summary`/`group_summary_en`). The table stays strictly pairwise and **canonical** (`fact_a_id < fact_b_id` CHECK kept), so insert/dedup/repoint/honesty are entirely unchanged and the unique constraint still blocks duplicate pairs. A group = all rows sharing `(person_id, base_fact_id)`; the contradicting fact of a row is whichever of `{fact_a_id, fact_b_id}` is not the base. **No new table.** (First cut reoriented `fact_a/fact_b` directionally + dropped the CHECK — rejected because it let `(X,Y)` and `(Y,X)` coexist and could make `RepointFactDuplicate` create duplicates; the pointer column avoids that entirely.)
- **Regroup pass** (`storage.RegroupPersonContradictions`, `backend/storage/contradiction_groups.go`): greedy "star peel" over a person's contradictions — repeatedly pick the highest-degree fact as the base and attach its incident edges as spokes; leftover non-star edges become their own group (lossless). Sets `base_fact_id` per row and (re)generates one LLM-synthesized merged summary per multi-fact group (`llm.SynthesizeContradictionGroupSummary`, free local GPU). New `dm contradictions regroup [--person] [--no-summary]` + a daily-pipeline step (5c, after detection+repoint). **Honesty score unchanged** — grouping is presentation-only; `bundle_stance_id` is never touched. Reads fall back to `fact_a_id` as base for not-yet-regrouped rows, so the view degrades gracefully to pairwise before the first regroup.
- **Read path returns groups.** `GET /api/stances/{id}/contradictions` now returns `ContradictionGroup` (base fact + N `contradicting_facts`, each with its reason + the merged summary), grouped by the BASE fact's stance (not `bundle_stance_id`) — which also surfaces previously-hidden cross-stance contradictions under the base's card. The per-stance badge now counts GROUPS (`GetStanceGroupCounts`), not raw pairs, while the per-stance score stays bundle-based. Frontend renders an anchor-centric `ContradictionGroup.svelte` card (base claim once, then its conflicts); the old `ContradictionPair.svelte` and `GetStanceContradictions` were removed.
- Lesson: detection is irreducibly pairwise (KNN + per-pair LLM/verify, and `fact_pair_checks` already stores every judged pair incl. negatives) — so the N-ary view is an *aggregation* of pairwise judgments computed by a regroup pass, not a change to the detector.

## 2026-06-08: Wayback Machine archival of source URLs (gh#16)

- New `backend/archiver` package + in-process background worker (started in `cmd/web.go` `executeServe`, ticks hourly). It pulls fact_sources lacking a snapshot (`archive_url IS NULL`, attempt older than 7d), checks the Wayback availability API, requests a save if absent, and records the snapshot URL. Decoupled from ingestion so archiving never blocks fact extraction. `archive_attempted_at` is stamped on every attempt (even failures) so a not-yet-indexed save isn't retried in a storm.
- Migration `20260608000000` adds `archive_url`/`archive_attempted_at` to `fact_sources` (+ partial index over the worker's predicate). `archive_url` now surfaces in `FactInStance`/`FactWithSource` and renders as a "Source" dropdown (show source / show archive) on the person page stance cards.
- Note: the bot's stale `claude/issue-16` branch was based on an old main and would have reverted recent work (translator, dedup, observability) — reimplemented fresh on main instead of rebasing.

## 2026-06-02: Entity dedup adjudication redesigned (whole-cluster -> pairwise + rule-based canonical)

- `dm persons dedupe` adjudication is now **pairwise**: the LLM is asked `{"same": bool}` per trigram candidate pair, and only CONFIRMED pairs are union-find clustered. The old flow clustered all trigram pairs first then asked ONE `same_entity` bool for the whole component — a prod dry-run on parties chained 88 unrelated entities (CA Democrats, Norway Centre Party, CDU, etc.) into one component and merged them all. Pairwise gating structurally prevents chaining through unconfirmed links.
- Canonical selection is now **rule-based, not LLM** (`storage.ChooseCanonical`): exactly-one-curated member always wins (never merge a junk dup over a real curated Assemblée entity); 2+ curated members skip the cluster; otherwise highest fact_count, tie-break lowest id. Removed the LLM `canonical_id` path that could pick a junk row as survivor and delete the real entity.
- Added a size cap (12) that skips any confirmed cluster larger than that with a `log.Warn` (defense-in-depth). Default `--threshold` bumped 0.4 -> 0.45.

## 2026-06-02: Daily pipeline scope fix — frpolitics votes-only, news/twitter queue-only

### What was done
- `runFrpoliticsStep` now runs frpolitics in `Mode: "votes"` (was `"both"`). In `"both"` mode, when no new laws passed the AI filter (the common case), frpolitics fell through to `runNewsSearchStandalone`'s `GetAllPersons` branch and searched news for **every entity in the persons table** (4,270 rows, incl. ~3,880 junk/foreign/duplicate entities auto-created from past mention sweeps). `PrioritizeTargets(PartiesFirst:true, LookbackDays:30)` cycled through the whole table ~73/night, so a French-politics pipeline was fact-checking the Norwegian Labour Party, Nigeria's APC, AfD village chapters, etc. This was the bulk of the ~1,200 facts/night and a big webcrawler-credit drain.
- Removed the `DAILY_FOR_ALL` env var and its branch in `dailyTargets`: news + twitter are now always queue-scoped (search_queue), matching the intended design.
- Pipeline is now simply: (1) news on queue, (2) twitter on queue, (3) frpolitics votes only, (4) stance merge, (5) contradictions, (6) photos, (7) audit.

### Lessons learned
- The news/twitter steps were correctly queue-scoped all along (~24 people). The unbounded scope came entirely from frpolitics' `"both"` mode news fallback. When wiring a domain plugin into the daily cron, give it its **own** narrow scope — never let a no-op day fall back to a full-DB sweep.
- `dm plugin fr-politics --mode news`/`both` (CLI) still has the `GetAllPersons` standalone path for manual full-DB runs; only the daily pipeline was changed.

## 2026-05-31: Stance-analyze 12K-context overflow fix + local-extractor UA rotation

### What was done
- **Stance analyze no longer overflows the model context (was silently dropping facts).** The analyze LLM call reserved a fixed 4000 output tokens against a base prompt that had grown to ~8000 tokens (it embeds the full ~25KB `promptrules.ContradictionRules`); 8K + 4K = the 12K ceiling, leaving ZERO room for key facts, so any non-trivial stance hit `ContextWindowExceededError` and the fact was rolled back ("facts without a stance" invariant). Fix in `backend/core/stance_manager.go`: (1) lowered the analyze output reservation 4000 → `stanceAnalyzeOutputTokens=2048`; (2) replaced the fixed `keyFactBatchSize=40` count-batching with `chunkKeyFactsByBudget`, which sizes each batch against the MEASURED base (`estimateStanceBaseTokens` measures `ContradictionRules` directly) so `base + facts + output + safety ≤ 12000` always holds — and can't go stale when the rules grow again. `keyFactBatchSize` stays as a hard backstop.
- **Local Go extractor now rotates User-Agents on failure** (`backend/extractor/local.go`). Was a single fixed Chrome UA; on a 403/anti-bot block it failed straight to the paid webcrawler fallback. Now tries Chrome → Firefox → Safari with a 1.5s pause between, returning on first success; 404/410 short-circuits the rotation. Recovers pages that block one fingerprint without spending webcrawler credits.

### Why
Surfaced by a live `dm plugin news search` run for Macron/Trump/Le Pen: the sync pipeline worked (a real Bardella retirement-age contradiction was detected, verified, and recorded), but ~5 facts were rolled back by the context overflow, and leparisien.fr 403'd the local scraper and fell through to webcrawler (which returned 402 = out of credits). Lesson (again): any fixed token budget against a prompt that embeds the shared rules will go stale as the rules grow — measure the dominant piece at runtime instead of hardcoding an assumption.

## 2026-05-31: Removed batch LLM processing entirely (gh#24)

### What was done
- Deleted the whole batch-LLM path: `cmd/batch.go` (the `dm batch` subcommands), `backend/llm/batch*.go` (BatchProvider + gemini/mistral impls), `backend/core/batch_job.go` (BatchJobOrchestrator), `backend/storage/batch.go` (batch storer), `backend/contradiction/batch_detector.go`, and all their tests. Removed the `web serve` batch poller and the `BATCH_PROVIDER`/`GEMINI_API_KEY`/`GEMINI_BATCH_MODEL` env vars.
- **Daily pipeline is now fully synchronous.** Step 1 was a batch submit (`collectAndSubmitBatch`) that required `BATCH_PROVIDER`; it's replaced by `runNewsCollection`, which extracts+stores news facts inline via the news provider's `CollectFacts` (OPEN_AI_API → lite-llm → local GPU). Twitter collection now always runs the sync path (`runTwitterCollection`); the old "skip sync when BATCH_PROVIDER set" branch is gone. Contradiction detection dropped its batch branch — always `processSyncContradictions`.
- Kept `backend/core/batch_extractor.go` — despite the name it's the content-scraping concurrency wrapper used by the SYNC news path, NOT batch-LLM. Removed `twitter.Provider.CollectContent` (returned `core.BatchContentItem`, only the batch path used it). Left the `llm_batches` migration/tables in place (goose history — dropping a migration file breaks boot); the unused tables are harmless.

### Why
The prod LLM endpoint is now a sync-only lite-llm proxy → local GPU (cachy), which has no batch API. `BATCH_PROVIDER` was therefore unset in prod, and the daily-pipeline cron called `newBatchProvider()` and crashed nightly with a FATAL (gh#24). Since batching can never work against this endpoint, the fix is removal, not a guard. News/Twitter ingestion is unaffected — it already ran through the sync extractors (that's why fresh facts existed despite the cron crashing); the pipeline now simply uses that working path directly.

## 2026-05-30: Adversarial verification pass before persisting contradictions (gh#23)

### What was done
- Every detector YES now passes through a second, independent **refutation** LLM call before it is recorded. If the refute pass returns NO the contradiction is dropped (never persisted, never dents the score). Single source of truth: the prompt lives in `backend/promptrules/verify.go` (`RefuteContradictionPrompt`, dependency-free) and the LLM call + parse in `backend/llm/verifier.go` (`VerifyContradiction`). Thin wrappers expose it on both `contradiction.OpenAIContradictionDetector` and `core.StanceManager` (the import graph `contradiction → categorizer → core` forbids the stance path from importing the detector, so the shared logic had to sit in the two dependency-free packages both can reach).
- Wired at all three persist sites: the stance path (`handleVerdict` + `AnalyzeFactInStance` filter entries via `verifyEntries`; if every flagged pair is refuted the verdict downgrades CONTRADICTION→NEW_INFO), the sync batch path (`cmd/contradictions_detect.go`), and the async Gemini batch path (`cmd/batch.go`). The recorded pair-check reflects the post-verification verdict so a refuted pair is not re-detected.
- **Fail-open on operational error**: a refute prompt that returns NO drops the pair; an LLM/parse *error* keeps it (logged WARN) — an infra hiccup must not silently delete a real contradiction. Model uncertainty resolves to NO via the prompt itself.
- New command `dm contradictions reverify` runs the refute pass over already-recorded rows. Read-only by default (prints what it would drop); `--apply` DELETES the drops but only after a confirming 2nd verification pass (2-of-2 drop required) to absorb the local model's run-to-run variance. Deletion clears the fact-pair check so the daily detector re-examines the pair later (self-correcting). (Distinct from `dm contradictions check`, which re-judges with the full detection rules; reverify uses the stricter FP-bucket refute prompt.)
- New local-only golden test `TestVerifyGolden` (`backend/contradiction/verify_test.go`) loads real prod cases from `testdata/verify_cases.yaml` and asserts keep/drop per case. Skips unless `OPEN_AI_KEY` is set (never runs in CI; LLM is the free local GPU). Add a misbehaving prod pair to the YAML and re-run to tune the prompt — ~15s, no DB, unlike `dm eval run` which is end-to-end (~20min, can't isolate the verifier from the detector).

### Why
A production review of all 44 contradictions found ~18-20% clear false positives in four buckets: numeric/measurement mismatches, different procedural scopes of the same action, conditional statements whose condition later came true (logic inversion), and same-direction claims flagged as opposed. The detector is a single LLM judgment with no second opinion, so any over-eager YES sticks forever. The refute pass is the highest-leverage fix (LLM is free locally; it only fires on the rare YES). Tuning lesson: a "default to NO when unsure" refute prompt is too aggressive on its own — it also kills genuine reversals (motivation flips, denial-vs-conviction, warned-then-voted, broken commitments). The prompt needs explicit POSITIVE patterns for real contradictions alongside the hard-NO FP buckets; balanced, the golden set is 12/12 (8 FP dropped, 4 TP kept) and stable across runs at temp 0.

## 2026-05-26: X-Application / X-App-Component headers on outbound LLM + embedding calls

### What was done
- `NewBaseLLMClient()` → `NewBaseLLMClient(component string)`. The `BaseLLMClient` in `backend/llm/client.go` (and the `backend/categorizer` re-export) now carries a per-instance `component` label, sent on every outbound request as `X-App-Component`. A constant `llm.AppName = "digital-mirror"` is sent as `X-Application` on every request. All 12 callsites pass a step name (e.g. `fact-extractor`, `stance-manager`, `contradiction-detector`, `twitter-discovery`, `embedder`). The embedding client (`backend/embedding/openai_embeddings.go`) also sets the same two headers directly.
- This is an API change: the constructor signature changed and there is no zero-arg variant. Any new callsite must identify itself.

### Why
The cachy GPU box runs gpu-watcher which surfaces which app + step is hammering the local LLM. Without these headers every digital-mirror call shows up as anonymous traffic and you can't tell whether it's the fact extractor, the stance manager, or twitter discovery driving load. Making the component argument required (no default) forces every future callsite to identify itself at the compiler level.

## 2026-05-25: Value-axis stances + values-based contradictions + dm scrape urls

### What was done
- **Values-based / directional contradiction rule** in `backend/contradiction/openai_detector.go`. A stated value/commitment ("rebuild artisanat") is contradicted by the person's own opposing action ("authorise offshoring") even when the two statements share no surface keywords. Worked examples: PAIR 30 (artisanat vs offshoring = YES), PAIR 31 (small symbolic counter-gesture = NO — requires material undermining). Narrowed the conflicting "different LEVELS of involvement" rule to apply only to CRIMINAL-EXECUTION cases.
- **Value-axis stance clustering** in `backend/core/stance_manager.go`. Topic-assignment, generateGeneralLabel, and refreshStanceMetadata prompts now recognise VALUE AXES as valid topic labels (e.g. "souveraineté industrielle française", "transition écologique", "libertés civiles et sécurité") so facts about different sub-topics that share a directional commitment cluster together. The MergeClusterDecision prompt was relaxed to allow merging value-axis siblings with explicit examples; still hard-blocks merges across different temporal/geographic contexts.
- **New CLI `dm scrape urls`** (`cmd/scrape.go`, `cmd/scrape_urls.go`, `storage.GetSourceURLsForPerson`). Reads URLs from a file (`--input`) or from an existing person's fact_sources (`--from-person`) and re-scrapes them into a content cache compatible with `dm ingest files`. Lets us refresh a person's corpus through new extraction prompts without paying for SerpAPI again.
- **New auditor token** `symbolic_counter_gesture` for the contradiction auditor enum, used when a flagged pair is a values-vs-action case but the counter-action isn't material enough.

### Why
The previous fixes (HARD GATE, DATE-ONLY rule, smoothed honesty) addressed the surface-level Macron false positive but left the deeper question: can we detect *indirect* contradictions — a values commitment broken by an opposing action on the same axis, with no keyword overlap? Two structural gaps were identified: (1) the prompt actively pushed the model AWAY from such pairs via the broad "different levels of involvement" rule, and (2) stance clustering was purely topical, so cross-topic facts on the same value axis landed in separate stances and the KNN-based contradiction detector never paired them. This change closes (1) with the dedicated rule + counter-examples, and partially closes (2) by teaching stance assignment + merge to recognise value axes. A residual gap remains: stances that fragment can still be hard to merge via centroid clustering alone — the merge command needs a wider Phase-A recall threshold (or a different clustering strategy) to surface value-axis siblings whose stance descriptions aren't lexically close.

### Validation on production
Ran the full pipeline against Macron after deploy: deleted 27 facts → rescraped 19 existing URLs via `dm scrape urls` (no SerpAPI cost) → re-extracted with the new prompt → ran 10-window 3-day SerpAPI scrape since 2026-04-27 (--max-parse 5/window, ~45 new articles) → contradiction detection → stance merge at 0.65 then 0.50 recall thresholds. End state: 76 facts, 19 stances, 0 contradictions. The original Stade-de-France false-positive does not recur. Score 100%. No real values-vs-action contradictions surfaced in this 4-week window — plausibly because Macron has been consistent in the period, not because the detector missed anything: the new value-axis language IS visible in stance-assignment LLM rationales ("the value axis of balancing protection with freedom of expression/creation"). Validation of the synthetic artisanat/offshoring case was not done — would require either injecting test facts or scraping a wider corpus.

## 2026-05-24 (followup): Prompt cleanup + dedup threshold drop

### What was done
- **PAIR 4 rewritten**: the old "Alice budget" example was ambiguous about whether Alice was the speaker. Now explicitly anchors both statements as Alice's OWN first-person claims, with REASONING that explains why the HARD GATE is satisfied. Stops the model from pattern-matching same-date numeric conflicts in third-party reports onto PAIR 4 and emitting YES.
- **Temporal rule 3 inverted**: was "analyze carefully for direct logical contradiction" (yellow light). Now defaults to NO; only flags YES when both facts are first-person claims by the same person about a fact they directly know.
- **PROMISE-BASED REASONING gated**: added a sentence stating that if neither fact is a personal claim/denial/commitment, there is no promise to break — the verdict is automatically NO. Stops the model from manufacturing promise/breaker structure where the facts don't contain one.
- **Insertion dedup threshold 0.85 → 0.80** in `backend/core/fact_inserter.go` (`DEDUPE_AUTO_THRESHOLD` env override). Same drop applied to the contradiction-detector's paraphrase-skip threshold in `cmd/contradictions_detect.go` for consistency. Safe because dedup is verb-aware: opposing-position pairs never dedup regardless of similarity.

### Why
Pair-of-pairs effect on the Macron case: even after adding the HARD GATE and DATE-ONLY rules, the prompt still contained PAIR 4 (training towards YES on same-date numeric conflicts) and a yellow-light Temporal rule 3 — those directly undercut the new gate. Lowering the dedup threshold addresses the upstream pipeline issue: the two Macron facts about the Stade de France finale were near-identical (verb-free, date-stripped embedding text) but sat just above the previous 0.85 cutoff. At 0.80 they would have collapsed at insertion and the contradiction layer would never have seen them.

## 2026-05-24: Stricter contradiction rules + smoothed honesty score

### What was done
- **Two new contradiction rules** in `backend/contradiction/openai_detector.go` (shared `ContradictionRulesAndExamples`):
  - **HARD GATE — UTTERANCE REQUIREMENT**: at least one statement must be uttered by the person themselves or be an authoritative finding about them. Pure third-party narratives on both sides are a SOURCE DISAGREEMENT, not a personal contradiction.
  - **DATE-ONLY CONFLICTS**: a bare date/timing discrepancy is NOT a contradiction unless it breaks a personal commitment to that timing. Three new example pairs (PAIR 27/28/29) anchor the rule and its carve-out (broken commitment about timing = still contradictory).
- **Two new auditor RULE tokens**: `third_party_narrative` and `date_only_no_commitment` added to the auditor enum and prompt.
- **Honesty score formula** (`backend/storage/postgres.go`): introduced `HonestyScoreMinDenominator = 75` Bayesian smoothing floor (`ratio = bundles / max(facts, 75)`), reduced `HonestyScoreRatioDecay` 4 → 1.5 and bumped `HonestyScoreAbsoluteDecay` 0.02 → 0.03. One contradiction now leaves a score around 95% and decays exponentially as bundles accumulate, instead of crushing a low-fact-count person to ~13%.

### Why
Production case: Macron had a single flagged contradiction (a date discrepancy between two third-party reports — neither was Macron speaking himself) and was sitting at 13% honesty. Both layers were broken: the detector was flagging pairs that don't satisfy the first rule (must be the person themselves or an authoritative finding), and the score formula was over-penalising tiny sample sizes via the ratio term.

## 2026-05-21: Stance contradiction temp 0.1 + periodic auditor pass + Grafana metrics

### What was done
- **Lowered stance-manager LLM temperature from 0.2 → 0.1** (`backend/core/stance_manager.go:720`). 0.2 was the lone outlier across 16+ JSON-classification call sites that all use 0.0 or 0.1; no commit message ever explained the choice. Verified on the AfD-Saxony-vs-Forsa-national false-positive case from today's prod run — 10 trials at each of temp 0.0 / 0.1 / 0.2 all returned NEW_INFO; live failure was a stochastic tail event.
- **Added periodic contradiction-audit pipeline step** (`cmd/pipeline.go`) — runs once every 5 days, gated by `job_metrics.created_at` lookup via the new `storage.GetLatestJobRunTime(jobType)` helper. Refactored `cmd/contradictions_check.go` so the audit logic is callable both from the CLI and from the pipeline (`executeContradictionAudit(ctx, storer, opts)` with `ContradictionAuditOpts`/`ContradictionAuditResult`).
- **Read-only by default**: the periodic pass identifies and logs false-positive contradictions but does NOT delete them. The intent is to watch the `contradiction_audits_total{verdict="remove"}` metric in Grafana for a few cycles before flipping on auto-delete. Each flagged row is logged at WARN with the contradiction ID, person, audit rule, and reasoning so operators can manually run `dm contradictions check --apply` (or eyeball the IDs) to clean up.
- **Two new Prometheus counters**:
  - `digital_mirror_contradiction_audits_total{verdict}` — labels: keep / remove / ambiguous / error
  - `digital_mirror_contradiction_audit_runs_total` — increments once per completed periodic pass (alert on stale = no movement in >6 days)
- Telegram daily-pipeline summary appends `🔍 audit: N checked, M flagged for removal` when the auditor ran.

### Why
A live frpolitics run today produced a false-positive contradiction (AfD reached 28% in a national Forsa poll vs AfD 42% record in Saxony — clearly different geographic scopes). Reproducing the exact LLM call against cachy 30×, the model never reproduced the false positive — so the prompt is correct and the failure is stochastic variance from the unjustified 0.2 temperature. With temp dropped to 0.1, false positives should become near-zero, but having the auditor sweep periodically gives recall coverage for whatever still slips through, plus a Grafana signal to watch quality drift over time.

### Operational notes
- The auditor pass uses the existing `OpenAIContradictionAuditor` + `ContradictionRulesAndExamples` — same rules as detection, so it's an apples-to-apples second read. LLM hits go to whatever `OPEN_AI_API` resolves to (cachy locally; nothing in prod yet because prod doesn't have OPEN_AI_API set — the periodic step will no-op there until that's wired).
- 5-day cadence is hardcoded in `runContradictionAuditIfDue(ctx, storer, 5*24*time.Hour)` in `cmd/pipeline.go`. Tweak there if the volume of contradictions makes 5 days too sparse or too aggressive.
- `job_metrics` rows for `job_type='contradiction-audit'` accumulate one row per metric per run — easy to historicize but should be kept in mind if you ever switch to upsert.

## 2026-05-21: Fact-extraction prompt slimmed + dynamic chunk sizing

### What was done
- **Prompt trimmed 18 304 → 15 164 chars (-17%)**: dropped the second (English) JSON example, consolidated the 4/4b/4c language rules into one paragraph, compressed `article_published_at`, slimmed redundant FR+EN-paired REJECT examples down to one language per rule. No content rule removed — only redundancy / verbosity. Net token cost (R=3 worst case) drops from ~6 100 to ~5 050 tokens.
- **Dynamic chunk sizing** in both `backend/categorizer/openai_extractor.go` (sync) and `backend/core/batch_job.go` (Gemini batch). At each call we render the prompt with empty Content, measure its size, and shrink the chunk so `prompt + chunk + 2 048 output + 256 safety ≤ 12 000` tokens. With today's prompt this caps chunks at ~13 600 chars; pre-trim it would have capped at ~10 800.
- **Reactive halve-and-retry** in the sync path: any `"maximum context length"` 400 from the LLM triggers a single chunk-split retry. Belt-and-suspenders for content where the tokenizer is denser than our chars/token estimate.
- **Static defaults lowered modestly**: `chunkSizeChars` 20 000 → 16 000, `chunkingThresholdChars` 25 000 → 18 000. The dynamic sizer almost always binds tighter; the new statics just stop us from blowing the budget if someone disables the dynamic path.

### Why
The prompt template grew 36% across PR #18 (multilingual: +3 071 chars) and ae83ecf (pub-date: +1 732 chars) without anyone re-tuning the chunker. The author's comment in `openai_extractor.go` had been claiming "~3-4k prompt tokens" since the 9 564-char baseline (60a104f) and was 50% stale by today. Real-world hit: today's news scrape failed ~4 article extractions with `9953 input + 2048 output = 12001 tokens, max=12000` — one token over. Dynamic sizing makes this impossible by construction; the prompt trim recovers ~1 050 tokens of chunk budget per call.

### Operational notes
- Both extractor paths now log `"dynamic chunk budget binds"` at DEBUG when shrinking — flip log level to debug to verify the right size is being used after any prompt edit.
- Constants live near the top of `openai_extractor.go` and `batch_job.go` (duplicated to avoid an import cycle). If you swap the local LLM for a larger-context model, bump `maxContextTokens` in BOTH files — the dynamic sizer will let chunks grow back toward `chunkSizeChars`.
- No env var added (per user preference). Model-context cap is hardcoded at 12 000 — matches the current self-hosted Gemma.

## 2026-05-18: Multilingual support — `lang` on facts + bilingual fact text

### What was done
- **Native-language across LLM-generated text** (ports PR #14's first 3 commits): contradiction detector, contradiction auditor, and stance manager prompts now include a `LANGUAGE RULE` telling the LLM to write summaries / labels / reasons in the same language as the input facts. `embedding_text` switched from English-only to native language (qwen3-embedding-8b is multilingual, so cross-language clustering still works). New `lang TEXT` column on `facts` tracks the source language (BCP-47); plumbed through `StoreFactInput`, `StanceFactInfo`, ingester, frpolitics (hardcoded "fr"), twitter (from tweet metadata).
- **Bilingual fact text** (new beyond PR #14): every extracted fact now carries both `fact_text` (native) AND `fact_text_en` (English translation). LLM prompt emits two new JSON fields: `lang` (REQUIRED, BCP-47) and `normalized_fact_en` (REQUIRED when lang≠en, OMITTED when lang=en). Parser drops facts with missing `lang` (matches the no-legacy stance). `fact_parser.go` updated with two new test cases (French-source-with-translation, English-source-omits-translation) plus a negative test for missing-lang rejection.
- **Scrape sidebar cleanup** (session work, separate commit): webcrawlerapi now requests `main_content_only:true` + new `stripTrailingSidebarSections` strips French-news H2 markers (`## Sur le même sujet`, `## À lire aussi`, etc.). News provider skips articles whose cleaned content is <200 chars to avoid LLM hallucination on BFMTV-style video pages with no real body.
- **Fact extractor prompt refinements**: Rule 3 carve-out clarifies CALLS/DEMANDS/ADVICE remain Type 1 stances even when a third party executes (vs FORECASTS, which are rejected). Anti-invention guard: don't fabricate facts about the primary subject if absent, but still extract facts about OTHER mentioned entities. `max_tokens` lowered 16384 → 2048 (typical extraction output ~1.5k tokens; keeps requests under gemma-4's 12k context).
- **One-shot tooling** (kept for future use): `dm facts audit-attribution` (rescrape every fact_source, drop those whose `original_quote` no longer appears), `dm facts rebuild-from-urls --person ...` (rerun extraction on URLs that previously produced facts for a person; resumable; supports `--urls-from-log` recovery).

### Why
- Generated text (contradictions, stances) was English-only regardless of source language — visible in the UI as French facts grouped under English summaries. The native-language switch fixes the consumer experience without losing semantic clustering (multilingual embeddings handle that).
- Bilingual fact text: future Twitter/cross-language analysis needs both versions; storing the English translation at extraction time is cheaper than re-translating on display.
- Sidebar cleanup: discovered that BFMTV video pages return only their "Sur le même sujet" related-videos block as "main content", letting the LLM extract sidebar tile headlines as if they were article facts. The "thin content" floor + local section stripper plug the gap.

### Operational notes
- Two migrations land: `20260512000000_add_lang_to_facts.sql` (ports PR #14) and `20260518000000_add_fact_text_en.sql` (new). Apply both before running any extractor against an existing DB.
- PR #14's 4th commit (`6af2ca3`) adding bilingual stance labels/summaries/contradiction-reasons + frontend 🌐 toggle is INTENTIONALLY NOT INCLUDED here — it goes beyond the stated ask ("stances/contradictions in NATIVE languages") and conflicts with main's evolved `stance_manager`. Can be added as a separate follow-up if you decide you want the dual-language frontend toggle.
- This PR supersedes #14.

## 2026-05-17: Serper.dev search provider + generic chained-provider abstraction

### What was done
- New `backend/search/serper.go` implementing `NewsSearcher`, `WebSearcher`, `ImageSearcher` against https://google.serper.dev. POST + JSON body, `X-API-KEY` header, supports SerpAPI-style `tbs=cdr:1,cd_min:M/D/YYYY,cd_max:M/D/YYYY` for historical news queries. Reads `SERPER_API_KEY`.
- New `backend/search/chain.go` with `ChainedNewsSearcher` / `ChainedWebSearcher` / `ChainedImageSearcher`: a generic meta-provider that takes a slice of `(name, provider)` pairs and tries each in order until one returns nil error. Replaces the bespoke `fallbackNewsSearcher` / `fallbackWebSearcher` types (deleted).
- `backend/search/provider.go` rewritten: `NewNewsSearcherFromEnv` / `NewWebSearcherFromEnv` / `NewImageSearcherFromEnv` now assemble a chain based on which keys are set, in priority order **Serper > Brave > SerpAPI**. Single-key configs still return a chain (1 entry) for uniformity. Logs the chain composition at startup so deploys are auditable.

### Why
- Switching news provider to Serper because it's cheaper than SerpAPI and crucially keeps `tbs` date-range filter support (Serpbase was the original candidate but lacks any historical-date parameter, breaking the daily pipeline's date-windowed queries). The chained abstraction was requested so adding/removing providers is just an env-var flip — no code change in callers.

### Operational notes
- To cut over: set `SERPER_API_KEY` on the host; existing `BRAVE_SEARCH_API_KEY` and `SERPAPI_KEY` remain valid as automatic fallbacks (clear them to remove from the chain). Boot log line "Configured news search chain" enumerates the active providers.
- `TestSerpAPINewsProvider_Integration` is currently failing locally with HTTP 429 — SerpAPI key is out of credits. Unrelated to this change (it's why we're switching).

## 2026-05-15: Sidebar pollution cleanup in news ingestion + audit-attribution one-shot

### What was done
- `backend/extractor/webcrawlerapi.go` now sends `main_content_only: true` on the `/v2/scrape` request and post-processes the markdown via `stripTrailingSidebarSections` to cut at the first French-news sidebar H2 (`## Sur le même sujet`, `## À lire aussi`, …). Note: `clean_selectors` is documented but rejected by `/v2/scrape` (probed 2026-05-15) — relying on `main_content_only` + local strip.
- `backend/plugins/news/provider.go`: added a `minArticleContentLen` (200 chars) skip guard in both the sync and the content-collection loops; new `SkippedThinContent` counter and `skipped_thin_content` metric label.
- New `dm facts audit-attribution` one-shot subcommand (`cmd/facts_audit_attribution.go`): re-scrapes every fact_source via the patched extractor, deletes rows whose stored `original_quote` is no longer a normalized substring of the cleaned content, and sweeps now-orphan facts through `DeleteFactsAndCleanup`. Resumable via `facts_audit_progress.json`; every action logged to `facts_audit_log.jsonl`. No dry-run mode — re-scraping is the expensive step, so the audit log IS the trail.

### Why
- BFMTV `video-*.html` pages have no real prose body. webcrawlerapi was returning the "## Sur le même sujet" related-videos sidebar as if it were the article — 14 tile-headlines per page. The LLM faithfully extracted each tile's quoted speaker line as a "fact" and the pipeline silently stamped the OUTER URL as the source on every one. Result: a Macron quote stored against a Le Pen URL, etc. The sidebar tiles also rotate, so the quote disappears from the page over time — leaving us with quoted facts we cannot verify.
- Confirmed empirically (Macron / Le Pen / Trump samples on the host DB) before fix. webcrawlerapi calls aren't free, so no dry-run on cleanup — the JSONL audit log replaces it.

### Operational notes
- After deploy, run the one-shot cleanup once: `host run --service mirror -- ./dm facts audit-attribution --batch-size 50 --delay-ms 200`. Review `facts_audit_log.jsonl`; expect a small-but-meaningful number of `DELETE_SOURCE`/`DELETE_FACT_CASCADE` rows. The daily pipeline now prevents new pollution at the source.
- `scripts/validate_extraction_cleaning.go` was a one-time fixture-validation harness — safe to delete after this lands.

## 2026-05-11: Self-hosted observability service (replaces Grafana Cloud)

### What was done
- New `observability` the host service built from `deploy/observability/Dockerfile`: colocated Prometheus + Grafana, supervised by s6-overlay, sharing one host volume at `/data`. Grafana is exposed publicly on the host-issued domain; Prometheus is bound to `127.0.0.1:9090` (only Grafana inside the same container can reach it). Prometheus scrapes `mirror.host.internal:9090/metrics` every 15s, retains 180 days of TSDB (~1 GB at current cardinality, well within the 5 GB volume). The 5 dashboards (`api-overview`, `business-metrics`, `database`, `llm-operations`, `system-health`) are baked in via Grafana provisioning.
- Deleted `backend/metrics/remotewrite.go` (~210 LoC of hand-rolled Prometheus remote_write protobuf encoding) and the remote_write goroutine in `cmd/web.go`. Removed the `PROMETHEUS_REMOTE_WRITE_URL/USER/PASSWORD` env vars. `go mod tidy` dropped `golang/snappy` and `protowire`.

### Why
- Grafana Cloud's free tier was slow to query and load — moving to self-hosted gives full control and snappy dashboards. Pull (scrape) over push (remote_write) is the standard model and lets us delete the custom protobuf encoder. Colocating Prometheus + Grafana into one container = one host service to manage instead of two.

### Operational notes
- Required host dashboard work (manual): create `observability` service from this repo with Dockerfile path `deploy/observability/Dockerfile`, attach a 5 GB volume at `/data`, set `GF_SECURITY_ADMIN_PASSWORD` + `GF_SERVER_ROOT_URL`, generate public domain. Remove the three `PROMETHEUS_REMOTE_WRITE_*` env vars from the `mirror` service. Local dev (`deploy/docker-compose.yml`) is unchanged — still runs separate Prometheus + Grafana containers, which is fine for development.

## 2026-05-10: Daily pipeline → the host cron service (no more in-process scheduler)

### What was done
- New `dm pipeline daily` CLI command (`cmd/pipeline_daily.go`) runs the daily pipeline once and exits non-zero on any step failure. `runDailyPipeline` now returns `error` (aggregated step errors via `errors.Join`).
- Deleted `startPipelineScheduler` entirely + its call site in `cmd/web.go`. Dropped the `PIPELINE_INTERVAL_HOURS` env var and the `envInt` helper. `dm web serve` no longer runs the pipeline on startup — solving the "every push to main re-runs the daily pipeline" footgun.
- `cmd/web.go` no longer constructs a top-level Telegram notifier (the scheduler was its only consumer; the balance alerter uses `alerterInstance` directly).

### Why
- the host's cron model requires the service to terminate, so a single service can't be both an always-on web server and a cron job. Splitting the daily pipeline into its own cron-scheduled the host service (`mirror-cron`, same image, start command `dm pipeline daily`) gives a real cron schedule and makes deploys idempotent (no pipeline side effect on restart).
- Exit-code-on-failure means the host's UI flags failed runs at a glance — Telegram remains for the detailed message.

### Operational notes
- Required host dashboard work (manual): create the new cron service (Empty Service → same repo, Settings → Cron Schedule + Start Command `dm pipeline daily`, copy env vars from `mirror` minus HTTP-only ones). Remove `PIPELINE_INTERVAL_HOURS` from `mirror` (now dead). See CLAUDE.md → Deployment.

## 2026-05-10: Daily pipeline consolidation (single ordered run)

### What was done
- Collapsed the two in-process schedulers (`PIPELINE_INTERVAL_HOURS` + `FRPOLITICS_INTERVAL_DAYS`) into a single daily run driven by `PIPELINE_INTERVAL_HOURS`. The standalone `runFrpoliticsPipeline` function and its ticker branch in `startPipelineScheduler` are gone; frpolitics now runs as step 3 inside `runDailyPipeline` via `runFrpoliticsStep`. `FRPOLITICS_INTERVAL_DAYS` is now a no-op (must be removed from the host env).
- `runDailyPipeline` runs in fixed order: (1) news fact extraction, (2) Twitter discovery + fact extraction, (3) frpolitics, (4) contradiction detection (`executeContradictionDetection` is now an explicit step, not just an implicit batch-chain side effect), (5) photo backfill.
- New `DAILY_FOR_ALL` env var: when unset/`0`, news + Twitter fact extraction iterate the `search_queue` (the "main persons queue"). When `1`, they iterate every person in the DB. `collectAndSubmitBatch`, `collectTwitterContentForBatch`, and `runTwitterCollection` now take an explicit target set built from `dailyTargets`. Twitter handle *discovery* still runs for all persons missing one (cheap, idempotent infra).

### Why
- Two schedulers + an implicit batch chain made it ambiguous what actually ran each day, and Twitter steps silently iterated every person regardless of the queue. Consolidating to one ordered pipeline + an explicit `DAILY_FOR_ALL` flag makes the daily contract obvious and matches the `search_queue` table's intent.
- All CLIs (`dm plugin frpolitics`, `dm contradictions detect`, `dm photos search`, `dm queue process`, `dm twitter fetch`) remain intact — only auto-schedulers changed.

### Operational notes
- Remove `FRPOLITICS_INTERVAL_DAYS` from the host env vars on the `mirror` service (it's a no-op now).
- Set `DAILY_FOR_ALL=1` if/when you want the daily run to cover every person; otherwise it stays scoped to `search_queue`.

## 2026-05-10: GitHub Actions CI (build + test + deploy gating)

### What was done
- New `.github/workflows/ci.yml` runs on every PR and push to `main`. Two parallel jobs: **`backend`** (Go 1.24 build + vet + `go test -race ./...` against a `pgvector/pgvector:pg17` service container, with all 37 `supabase/migrations/*.sql` applied via `psql`) and **`frontend`** (Node 22 + `npm ci` + `npm run check` + `npm run build` inside `frontend/`).
- Heavy caching everywhere: `setup-go` caches `~/go/pkg/mod` + `~/.cache/go-build`; `setup-node` caches `~/.npm`; explicit `actions/cache` over `frontend/node_modules` (skips `npm ci` on hit) and `frontend/.svelte-kit` + `frontend/node_modules/.vite` (with `restore-keys` for partial hits). `concurrency: cancel-in-progress` kills stacked runs on force-push.
- CI does NOT build the Docker image — the host's BuildKit handles that with its own layer cache; duplicating it in CI would double cost without catching anything new.

### Why
- Repo had zero build/test CI before this — pushes to `main` deployed to the host unchecked, so a broken commit shipped to production. Gating the host deploys behind GitHub status checks (the host's built-in "Wait for CI to pass" toggle, no `DEPLOY_TOKEN` needed) closes that gap.
- Tests that hit external APIs (OpenAI, SerpAPI, Brave, Firecrawl, Apify, WebcrawlerAPI) self-skip via `t.Skip()` when their env var is unset, so CI runs without secrets.

### Operational notes
- After this lands, manually enable "Wait for CI to pass" on the `mirror` and `frontend` the host services (Service → Settings → Source). Optionally add `ci / backend` and `ci / frontend` as required status checks under GitHub branch protection for `main`.
- First CI run may be partially red — the Go test suite has never been gated. Triage as a follow-up: fix or add a justified `t.Skip()` per failure, don't mass-disable.

## 2026-05-10: Photos backfill in daily pipeline

### What was done
- Added `runPhotosBackfill` to `cmd/pipeline.go`. It runs as the final step of `runDailyPipeline` (driven by the in-process `PIPELINE_INTERVAL_HOURS` scheduler in the `mirror` the host service), so every cycle picks up any persons/parties still missing an `image_url` via SerpAPI image search → R2 upload → DB write. No new env var, no host dashboard config needed — relies on the same scheduler that runs news + Twitter collection.
- Gracefully no-ops (warn-log) if `OPEN_AI_KEY`/SerpAPI or R2 is not configured, mirroring how `runTwitterCollection` degrades. Errors bubble up into the daily pipeline's Telegram error notification under a `photos:` prefix and are recorded as `photos-backfill` job metrics (uploaded/skipped/errors).

### Why
- We had no automated way to fill in photos for newly-added persons (deputies imported by frpolitics, persons mentioned in news). The CLI `dm plugin photos search --all` existed but had to be triggered manually. Making it a pipeline step means the photo coverage converges to 100% on its own.
- Chose the in-process scheduler over a separate the host cron service to stay consistent with the existing pattern (Twitter discovery, news collection, frpolitics all live inside `runDailyPipeline`/`runFrpoliticsPipeline`) and to avoid the host dashboard/service-creation steps.

## 2026-05-07: Stance-first profile page

### What was done
- Profile page (`frontend/src/routes/person/[id]/+page.svelte`) is now stance-first: the old Facts / Contradictions / Stances tab strip is gone. Stances are the headline content, each rendered with a small circular gauge for a per-stance honesty score, expandable to lazy-load that stance's contradictions inline. Facts moved behind a single de-emphasized "Show all N facts" disclosure. The Contradictions tab is removed entirely (contradictions are revealed *inside* their stance).
- Hero gets a one-line summary under the name: `X stances · Y contradictions over Z facts`.
- Stance list has a client-side sort selector: worst-first (default), most-recent, most-facts.
- Reusable `ScoreGauge.svelte` and `ContradictionPair.svelte` components extracted; the hero gauge, score-breakdown gauges, and per-stance gauges all use the same component.

### API changes
- `GET /api/person/{id}/stances` now also returns `score` (0..1, same scale as the global honesty score) and `contradiction_count` per stance.
- `GET /api/person/{id}` now also returns `stance_count` and `contradiction_count` (used for the hero summary line so it doesn't need to wait for the stances list).
- New endpoint `GET /api/stances/{id}/contradictions` returns the contradictions whose `bundle_stance_id` matches the stance — used to lazy-fill a stance card on expand. No pagination (stance bundles are small by construction).
- New storer methods: `GetStanceContradictionCounts(personID)` (map[stanceID]count) and `GetStanceContradictions(stanceID)`.

### Why
- Stances bundle related facts and the contradictions among them, so they carry far more signal per pixel than a flat fact or contradiction list. Leading the page with stances surfaces the highest-signal information first.
- Per-stance score reuses `ComputeHonestyScore()` with stance-scoped inputs (`facts = stance.fact_count`, `bundles = 1 if any contradictions else 0`, `total = raw count`) so each stance's gauge reads on the same 0..1 scale as the headline score — no bespoke formula to explain.

## 2026-05-07: Orphan-fact stance assignment (`dm facts assign-stances`)

### What was done
- New CLI subcommand `dm facts assign-stances` finds every fact not linked to any `stance_document` (the `facts_without_stance_total` invariant) and routes each one through `StanceManager.ProcessNewFact` — the same path production ingest uses. Per-person flow:
  1. `ListOrphanFactsForPerson` (new storer method) → list of orphans for the person.
  2. `ProcessNewFact` per orphan → assigned to existing stance via LLM topic-classifier, or new stance created.
  3. `rebundlePerson` (refactored shared helper) → SQL fast path + LLM Bundler over remaining solos for that person.
- After all in-scope persons are done: a DB-wide `rebundleAll` pass (`BundleBackfillFastPath` + LLM Bundler over every solo). `--skip-final-rebundle` opts out.
- Two new storer methods on `PostgresStorer`:
  - `ListPersonsWithOrphanFacts(ctx)` — drives the per-person worker loop.
  - `ListOrphanFactsForPerson(ctx, personID)` — loads orphans only, dedup-filtered like `loadFactsForBackfill`.
- Refactored `cmd/stances_rebuild.go::rebundleContradictionsForPerson` into `cmd/stances_rebundle.go::rebundlePerson` so the same logic is shared between `stances rebuild` and `facts assign-stances`. No behavior change.
- Updated the invariant warning in `metrics/invariants.go` to point at `dm facts assign-stances` (the correct remediation; `stances rebuild --resume` skips persons with ≥1 stance and so cannot fix orphans for them).

### Why
- `dm stances rebuild --resume` skips any person who already has at least one stance, so orphan facts inserted *into a person who already has stances* never get processed by the rebuild path. Today's session left ~1,359 such orphans on prod.
- The fix isn't to change rebuild's resume semantics (those make sense for "rebuild from scratch") — it's a separate, targeted command that operates only on orphans and reuses the live ingest path verbatim.
- Atomicity is **idempotent per-fact**: `AddFactToStance` is `ON CONFLICT DO NOTHING`, `RecordContradiction` is unique-constrained on `(fact_a_id, fact_b_id)`. A crash mid-run leaves a self-consistent partial state and re-running picks up exactly the still-orphan facts. Per-person tx wrapping was considered but rejected as over-engineering for an idempotent batch operation.

### Operational notes
- Run `--dry-run` first to confirm the orphan count matches the `FactsWithoutStance` gauge.
- Re-running after success should report 0 persons in scope.
- The DB-wide rebundle at the end catches contradictions whose two facts now live in *different* stances (cross-stance) — only the LLM Bundler can pick a home for those. Anything still solo after this is genuinely declined by the Bundler.

## 2026-05-07: Stance LLM-merge phase (`dm stances merge`)

### What was done
- New subcommand `dm stances merge` runs a **two-phase per-person merge**:
  - **Phase A** (in-Go, no LLM): agglomerative average-linkage clustering on the stance centroids (cosine distance). Cut at `--recall-threshold` (default `0.65` similarity / `0.35` distance). Lance-Williams update keeps it O(N² log N). Singletons dropped, oversized clusters (> `--max-cluster`, default 30) sub-clustered at a stricter `--subcluster-threshold` (default 0.75 sim).
  - **Phase B** (LLM): for each multi-stance candidate cluster, one prompt asks the model whether subsets actually represent the same stance and to emit a unified summary + label per merge subgroup. Each subgroup is then folded via `MergeStancesForPerson` (largest fact-count wins as dst; weighted normalised centroid) and the LLM-returned summary/label is written through the new `UpdateStanceSummary` storer method.
- Flags: `--person` / `--persons-file` (mutually exclusive), `--parallel`, `--dry-run`, `--recall-threshold`, `--subcluster-threshold`, `--max-cluster`.
- Storage: added `PostgresStorer.UpdateStanceSummary(ctx, id, summary, clusterLabel)` (only the merge phase needs it; the storer method intentionally does **not** know about LLM concepts beyond text).
- Core: added `StanceManager.MergeClusterDecision` + `StanceForMerge` / `MergeDecision` / `MergeSubgroup` types. Validates each subgroup (≥ 2 known IDs, non-empty summary/label) before applying.

### Why
- The 0.85 cosine-similarity centroid post-pass added in the rebuild commit catches almost nothing (5 / 1737 in round 1) because two stances both about e.g. "AfD on immigration" with different framings sit at cosine ~0.7-0.8 — under the cutoff but semantically the same stance.
- Lowering the centroid threshold uniformly was rejected: at 0.78 you start merging genuinely-distinct stances (X on immigration policy vs X on border security) and damage contradiction detection.
- Splitting the decision into two phases lets us be loose where it's cheap (recall via clustering) and tight where it matters (precision via LLM). The LLM never sees the full stance list — only one small candidate cluster at a time — so the 8K vLLM context is never an issue, regardless of how many stances a heavy political figure has.

### Why average linkage (not single-link / UnionFind)
- Single-link causes "chaining": A close to B, B close to C → all merged transitively even if A-C is far. Snowballs into mega-clusters at loose thresholds.
- Average linkage uses the mean distance to all cluster members, so a cluster only absorbs new members that are reasonably close to the *whole* cluster. This is the property we want when the threshold is intentionally loose.

### Operational notes
- The merge is idempotent — safe to re-run. `MergeStancesForPerson` is transactional, fact memberships move with `ON CONFLICT DO NOTHING`, contradictions repoint before the src row drops.
- Sub-clustering chunks (when even the sub-pass produces > `--max-cluster`) fall back to deterministic by-index slicing with a warning. Should be rare in practice.
- LLM failures on a single cluster are logged and counted; they don't abort the person.

## 2026-05-07: Per-fact stance rebuild + centroid merge

### What was done
- Replaced the batched LLM topic-grouping step in `dm stances backfill` / `dm stances rebuild` with a per-fact loop that reuses the production-ingest path (`StanceManager.ProcessNewFact`). Facts sharing an exact `embedding_text` still pay only one LLM call (the rest are joined via `AddFactToStance`).
- Added a post-pass that merges a person's stances whose normalised `cluster_embedding` centroids exceed `--merge-threshold` (default 0.85) cosine similarity. UnionFind handles transitivity. The representative is the largest-fact-count stance; the new centroid is the weighted normalised mean of all members.
- New transactional storer method `MergeStancesForPerson(srcID, dstID, newCentroid)` moves fact memberships, repoints contradictions whose `bundle_stance_id` was src **before** the row is dropped (FK is `ON DELETE SET NULL`), recomputes `fact_count`, and deletes src.
- Deleted now-unused code: `StanceManager.GroupTopicLabels` + `TopicGroupingResponse` + `TopicGroup`, `GenerateSummary` + `summaryLLMResponse`, the `applyTopicGrouping` / `findBestGroupMatch` / `factCluster` helpers in `cmd/stances_backfill.go`.
- New flag `--merge-threshold` on both `stances backfill` and `stances rebuild`. Rebuild summary printout gains `Stances merged (centroid)` and `Contradictions detected (new)` lines.

### Why
- The batched `GroupTopicLabels` LLM call packed every unique label for a person into one prompt that easily exceeded 8K tokens for politicians with hundreds of facts. Local vLLM (`gemma-4-26b-a4b-it` on cachy, RTX 5080 16GB) is launched with `--max-model-len 8192` and is already CPU-offloading 8GB — bumping the context cap is not safe. Chunking the call would have produced fragmented groupings unstable across runs.
- Per-fact assignment fits trivially: the prompt is ~`200 chars × N existing stances`, not `N labels × full label text`. Centroid-based merge after the fact recovers the across-group consolidation that the batched call was supposed to provide, but using the embedding signal we already trust (the LLM was redundant with it).

### Operational notes
- Re-running `stances rebuild` will re-record contradictions; `RecordContradiction` is idempotent on `(fact_a_id, fact_b_id)`. Newly-discovered contradictions WILL fire SSE events and trigger honesty-score recomputation — this is consistent with normal ingest behaviour and arguably catches contradictions the prior coarse grouping missed.
- `--resume` skips persons that already have stances and therefore also skips the merge pass for those persons. Use without `--resume` for a clean rebuild, or run merge-only later if/when needed.
- New flag `--persons-file` on `stances rebuild` accepts one person name per line, resolved via `core.ResolveTargetName` (exact match only; fuzzy matches are skipped with a warning). Mutually exclusive with `--person`. Useful for targeted retries / mop-ups.

### Bug fix in this batch
- `StanceManager.createNewStance` was byte-truncating the topic label at 120 (`label[:120]`), which cut multi-byte UTF-8 codepoints in half (French é/à/ü, German Umlauts, Arabic, Cyrillic) and surfaced as `ERROR: invalid byte sequence for encoding "UTF8"` (SQLSTATE 22021) on the INSERT. Switched to rune-aware truncation. Lesson: any `s[:N]` on text that could contain non-ASCII must go through `[]rune` first.

## 2026-05-05: Strict closed-enum LLM contracts + 16k-safe contradiction batching

### What was done
- Tightened the structured-output contracts on three LLM call sites so model responses are validated against a closed enum and **rejected (with one strict retry) when out-of-spec**, instead of silently normalised:
  - `backend/core/fact_auditor.go` — `RULE` must be one of 9 tokens (`none | not_about_entity | lawyers_as_subject | speculative | not_contradictable | journalist_characterization | pattern_of_behavior | vague | polls_or_surveys`). Added cross-check: `VERDICT=keep ↔ RULE=none`.
  - `backend/contradiction/auditor.go` — `RULE` must be one of 15 tokens (`none | accusation_not_finding | learning_over_time | …`). Same retry + cross-check semantics.
  - `backend/core/fact_extractor.go` — `canonical_verb` and `subject_entity_type` are declared as MUST-be-exactly-one-of lists in the prompt. `categorizer/fact_parser.go` now drops the entire fact (with WARN) when `canonical_verb` is empty or outside the 20-verb enum, and normalises `subject_entity_type` to `"human"` (lower stakes — only metadata) when out-of-enum.
  - `backend/core/relation_classifier.go` — unknown `relation` values were silently dropped; now logged as WARN before drop.
- Added strict retry path on both auditors: on first malformed reply, the auditor sends ONE corrective follow-up (`"Your previous reply was rejected: <reason>. Reply with EXACTLY: …"`) appending the bad reply to the conversation so the model sees what it got wrong. If the second reply is still bad, the result surfaces an error rather than producing a fake-canonical verdict.
- Reduced contradiction-detector context footprint to keep prompt + max_tokens reservation under 16k:
  - `MaxPairsPerBatch` reverted from 5 → **3** (the comment had already flagged "monitor for cross-pair confusion at 5").
  - Live detector `MaxTokens` lowered from 16,384 → **2,048** (realistic 3-pair output is ~600 tok; 2k leaves headroom).
  - Contradiction auditor `MaxTokens` lowered from 8,192 → **1,024** (3-line response).
  - Fact auditor was already at 1,024.

### Why
- A 200-fact gemma audit run emitted four spellings of the same RULE (`vague` / `vague_or_not_concrete` / `rule_6_vague_or_not_concrete` / `vague (rule 6)`), fragmenting the rule-frequency view. The first reflex — substring-match the variants back to the canonical token — would silently hide cases where the model is reasoning OUTSIDE our enum entirely. The strict-with-retry path forces the model to either produce a token we recognise or fail loudly so the bad fact gets logged and skipped instead of mislabelled.
- The 16k cap matters because we may want to point the contradiction detector at smaller-context models (e.g. local llama-swap) for cheap pre-filtering. With prompt ~2.5k tokens + max_tokens 16,384, the live detector reserved ~19k of context. 3 pairs × 2k max_tokens reserves ~4.5k, comfortably fitting an 8k window.

### Operational notes
- The strict retry costs at most one extra LLM call per malformed first reply.
- If the second reply is still bad, the audit JSONL row carries `error: "LLM returned malformed response twice (strict): …"` — these should be inspected manually rather than acted on.
- The `relation_classifier` change is observable: pipeline runs that previously silently dropped unknown relations now emit a WARN log, surfacing previously-hidden classifier drift.

## 2026-05-04: `dm facts audit` — LLM-guided fact auditor with cascade cleanup

### What was done
- New CLI `./dm facts audit [--person … | --person-id … | --fact-id …] [--max N] [--random] [--apply] [--endpoint URL] [--model M] [--apikey K]`.
- New auditor `backend/core/fact_auditor.go::OpenAIFactAuditor`. The audit prompt embeds the WHOLE `CoreFactExtractionPrompt` verbatim (rendered via `BuildFactExtractionPrompt` with audit-mode placeholders), wrapped with a frame that says "evaluate THIS existing fact, do not extract new ones; verdicts: keep / delete / unsure". This guarantees the audit rules cannot drift from the extractor rules — when the extractor prompt is updated, the audit picks up the new rules on the next call.
- Audit prompt threads `source_published_at` and `fact_created_at` into the fact-under-audit block, and clarifies rule 6 ("vague"): a fact lacking an inline date is NOT vague when the audit context already provides a publication / insertion date. This avoids spurious "vague" deletes on facts like "Trump waived US shipping law for oil and gas" that have a known source date in `fact_sources.published_at`.
- New storage helpers: `ListFactsForAudit(personID, factID, random, limit)` aggregates persons and source metadata into one row per fact; `DeleteFactsAndCleanup(factIDs)` runs the destructive path in a single tx — `DELETE FROM facts WHERE id = ANY($1)` (FK CASCADE handles `contradictions`, `stance_document_facts`, `fact_sources`/`fact_mentions`, `fact_relations`, `fact_pair_checks`), then drops `stance_documents` that became empty as a result, then recomputes own_score / members_score / aggregate for each affected person and emits `person_updated` events with `reason: fact_audit_deletion`.
- `--endpoint` / `--model` / `--apikey` overrides exist explicitly so the auditor can point at a local LLM gateway (e.g. `http://cachy:8080/v1` over Tailscale) for cheap experimentation against multiple models without touching the live `OPEN_AI_*` env vars.

### Why
- The 2026-05-04 magnet-stance incident on Trump revealed that historically-extracted facts contain a long tail of rule-violators (meta-observations, journalist characterisations, conditional threats incorrectly normalised, …). Re-running the extractor over each source article would require re-scraping and would not converge — the same prompt that produced the bad fact would just produce it again. An LLM auditor that reads the EXISTING facts under the CURRENT rules separates "what we have" from "what we should have" without any re-scrape.
- Default = read-only JSONL: keep / delete / unsure verdicts plus reasoning per row, summary on stderr. `--apply` is gated by an interactive confirm (skip with `--yes`). This makes the tool safe to run repeatedly with different models / sample sizes for calibration before touching the database.

### Operational notes
- On gemma-4-26b-a4b-it (cachy llama-swap, 8k ctx) a 50-fact Trump sample takes ~6 min and reports ~25-33% delete rate, suggesting a wide gap between historical and current rules. Several borderline cases vary run-to-run at temp=0.1 — for a real bulk apply, sample 2-3 times and majority-vote, or use a stronger model (Sonnet/Opus) for the final pass before deletion.
- Known false-positive class: gemma sometimes flags the entity's own *conditional threats* (e.g. "Trump said he would strike if X by 20:00") as `speculative` (rule 3). Conditional promises ARE Type 1 — worth tightening rule 3 in the extractor with an explicit carve-out before relying on the audit verdict.
- Cascade is "delete cleanly" by design: every contradiction touching a deleted fact disappears, every stance that becomes empty is dropped, every affected person's honesty score is recomputed in the same tx. Re-running `dm stances rebuild --resume` after a bulk apply is still recommended to absorb facts that were already orphaned (the 2,673 stance-less Trump facts pre-existing this work).

## 2026-05-04: Reject meta / pattern-of-behavior facts and stance labels

### What was done
- Fact extractor (`backend/core/fact_extractor.go`): added REJECT rule 5b that explicitly bans pattern-of-behavior / meta-observation inputs ("X has repeatedly contradicted his announcements", "X has a long history of denying climate change"). Listed RED FLAG phrasings ("repeatedly", "régulièrement", "multiplié", "tends to", "has long claimed", "is known for", "pattern of", "every time"). Also tightened the `embedding_text` rule to forbid form-words ("announcements", "contradictions", "denials", "rhetoric", "pattern") as the topic fingerprint — the embedding must point at a SUBJECT.
- StanceManager `assignTopic` (`backend/core/stance_manager.go`): tightened the `new_topic_label` LABEL RULES with the same subject-vs-form distinction and an explicit list of bad meta labels ("policy announcements contradictions", "campaign promises", "denials and reversals", "flip-flops on foreign policy", …). Pairs with the earlier prompt fix in `GroupTopicLabels`.

### Why
- The Trump rebuild produced a single-fact stance "policy announcements contradictions" anchored on a journalist commentary fact ("Donald Trump a multiplié les annonces grandiloquentes qu'il a régulièrement contredites"). The stance summary then literally described the meta-pattern of "promises he reverses", so the LLM Bundler happily attached 26 unrelated subject-specific contradictions (Iran war, Strait of Hormuz, Venezuela, Iran negotiations, …) into it. Two distinct mistakes were chained: (a) ingesting a meta-observation as if it were a fact; (b) labelling its stance with a meta name that then magnetises other contradictions.
- The fix is at both layers: refuse the meta-fact at extraction (root cause), AND refuse the meta-label at stance creation (defence in depth, in case extraction lets one slip through).

### Operational notes
- Existing meta-facts and meta-stances stay until cleaned up. The user-facing impact is limited (one or two magnet stances per person), but they should be deleted via SQL or a future audit pass before the next full rebuild.

## 2026-05-03: `stances rebuild` is now atomic (rebuild + rebundle in one pass)

### What was done
- `./dm stances rebuild` now performs all three steps per person in a single run: (a) delete existing stances, (b) re-cluster facts into fresh stances, (c) Phase 1 SQL fast-path bundle (`BundleBackfillFastPath`) over that person's contradictions, (d) Phase 2 LLM Bundler over remaining solo contradictions. Both phases are scoped to the person being rebuilt — the `personID *int64` argument plumbed through `BundleBackfillFastPath` / `ListSoloContradictionsForBundling` is now always set during a rebuild.
- The standalone `./dm contradictions bundle-backfill` subcommand is removed. Its body lived in `cmd/contradictions_bundle_backfill.go`; the file is deleted. All bundling is now triggered either inline at insert (sync/batch contradiction detection) or as part of `stances rebuild`.
- Summary printout gained `Contradictions rebundled (SQL)` / `Contradictions rebundled (LLM)` / `Contradictions left solo` lines so a rebuild's bundling effect is visible without a separate audit.

### Why
- Deleting and re-creating stances wipes `contradictions.bundle_stance_id` (FK has `ON DELETE SET NULL`), leaving every contradiction as a solo bundle until something re-bundles. Forcing the operator to remember a second command was a footgun: a half-completed rebuild left the DB in a worse state than before — correct stances, but every honesty score temporarily inflated by the un-bundled contradictions. Doing it per-person inline means a `^C` mid-run leaves earlier persons fully consistent, and the never-touched persons untouched.
- Per-person scoping of Phase 1/2 (vs. running them globally at the end) keeps a single person's blast radius isolated and parallelises naturally with `--parallel N`.

### Operational notes
- `--dry-run` skips both bundling phases (no DB writes anywhere). `--resume` still skips persons with existing stances; bundling for those persons is not re-run by `stances rebuild` — use a real rebuild if their bundles need to be recomputed.
- Running `stances rebuild` now requires `OPEN_AI_KEY`/`OPEN_AI_API` to be set even if no new facts are ingested, because the LLM Bundler is constructed up-front.

## 2026-05-03: Stance topic-grouping prefers subject over rhetorical method

### What was done
- Tightened the LLM prompt in `core.StanceManager.GroupTopicLabels` (`backend/core/stance_manager.go`) with explicit subject-vs-method rules. Bad group names like "policy announcements", "campaign promises", "denials and reversals" are now called out as forms (HOW the figure communicates) and rejected. Good group names ("US tariffs on Chinese goods", "NATO Article 5 commitment", "parliamentary assistants case") describe WHAT the labels are about. Default leans toward splitting by subject; merging only when subjects truly match.

### Why
- A rebuild on Donald Trump produced a single "policy announcements contradictions" bundle of 26 pairs spanning tariffs, NATO, immigration, and healthcare — the model had grouped by *speech act* rather than *topic*. That collapses unrelated disagreements into one bundle, defeating the point of the bundling layer (under-counting distinct lies). The fix is at the topic-clustering boundary, not at bundling: once stances are subject-scoped, contradictions naturally bundle along the right axis.

### Operational notes
- Existing stance documents created before this change are unaffected until rebuilt. Run `./dm stances rebuild --person "Donald Trump"` to validate, then `./dm stances rebuild --parallel 8` for full re-clustering once the per-person test looks right.

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

## Entity deduplication (persons merge + dedupe)

- New API on `*PostgresStorer` (`backend/storage/person_dedup.go`): `MergePersons(from,into)` folds one
  person into another in a single tx — conflict-safe FK repoints across all 10 referencing columns,
  stance memberships moved BEFORE stance rows (preserves "every fact has a stance"), then survivor
  score recompute via existing `recomputeScoreAfterFactDeletion`. `FindDuplicateCandidates` (pg_trgm)
  and `GetPersonsForDedup`.
- New CLIs: `dm persons merge --from --into` and `dm persons dedupe [--type --threshold --apply]`
  (dry-run by default; clusters trgm pairs via union-find, LLM-adjudicates each cluster).
- LLM adjudicator `core.EntityDedupAdjudicator` + reusable `core.ClusterPairs` union-find.
- Migration `20260602000000_entity_dedup_trgm.sql`: pg_trgm + unaccent + IMMUTABLE `immutable_unaccent`
  wrapper + functional index `lower(immutable_unaccent(name))`. Insert-time person lookups
  (`FindOrInsertPersonWithType` and batch `findOrInsertPersons`) are now accent+case-insensitive, so
  "éric zemmour" no longer creates a duplicate of "eric zemmour". Go-side fold key: `util.NormalizeKey`.

### Lessons learned
- The IMMUTABLE unaccent wrapper MUST use the 2-arg `unaccent('unaccent'::regdictionary, $1)` form
  (1-arg unaccent is only STABLE) AND pin `SET search_path` on the function — otherwise the functional
  index build fails with "text search dictionary unaccent does not exist" (index builds run with a
  reset search_path). The `::regdictionary` cast is required; a bare string literal fails.
- `supabase db reset` is INCOMPATIBLE with our goose-annotated migrations: it executes each file's
  `-- +goose Down` block as raw SQL, so `ADD COLUMN ... ; DROP COLUMN ...` migrations net to nothing
  and the persons-lang backfill then fails on a missing `facts.lang`. Use the goose path
  (`migrate.Run`, what the app/`dm web serve` uses) for a clean local DB, not `supabase db reset`.

## Daily-pipeline observability + verifier FP rules (2026-06-04)

- **`pipeline_runs` table + `RecordPipelineRun`**: `dm pipeline daily` now writes one
  durable summary row per run (status, timing, news stats, audit counts, and
  window-derived facts/contradictions/persons counts). Rationale: the cron service is
  ephemeral, so Prometheus cannot scrape it — Postgres is the source of truth for
  pipeline-run history. Grafana reads it via a new `MirrorDB` Postgres datasource and the
  "Digital Mirror - Daily Pipeline" dashboard (incl. a last-run contradictions drill-down).
- **Verifier (`promptrules/verify.go`)**: added two false-positive NO-rules —
  (7) endorsing another person for office ≠ contradicting "I'm not a candidate", and an
  extension to (5) that an action which *fulfills/advances* a stated goal/trend is
  follow-through, not a reversal. Golden cases FP_144 / FP_147 added.

## Observability service deployed + metrics bind fix (2026-06-05)

- **Observability service is now live on the host** (Grafana + Prometheus, `deploy/observability/`),
  with a `/data` volume and the `MirrorDB` Postgres + Prometheus datasources. Deploy from the subdir
  via `host up deploy/observability --path-as-root --service observability`.
- Fixed a latent crash: the `init-perms` s6 **oneshot** `up` file is parsed by *execline*, not
  `/bin/sh`, so the old multi-line shell script mis-parsed (`mkdir` swallowed `chown -R`'s `-R`).
  It's now a single execline line invoking `/bin/sh -c "..."`. Never caught before because the
  service had never been deployed.
- **Metrics server now binds `[::]:9090`, not `127.0.0.1`** (`cmd/web.go`). the host's private network
  is IPv6-only, so loopback was unreachable from the observability service's Prometheus
  (`up{job="mirror"}==0`). Requires a `mirror` redeploy to take effect.

## LLM gray-zone fact dedup (2026-06-05)

- **Fact dedup is now tri-band.** Embedding cosine similarity `>= 0.80` still auto-dedupes
  (verb-aware); `< 0.70` is always kept; the **gray zone [0.70, 0.80)** — plus any
  similar-but-different-verb pair — is handed to an LLM adjudicator (`core.FactDedupeAdjudicator`,
  `IsSameClaim`) that answers "same claim?". Fixes the class where a genuine restatement with a
  different reporting verb (PROMISED vs CLAIMED, sim 0.78) slipped under the auto line and produced a
  duplicate contradiction (#145/#146). The adjudicator is **conservative by design** — a wrong "same"
  deletes real info, so it defaults to distinct and explicitly never merges contradiction-shaped
  pairs (promise vs contrary action, before/after, supports/opposes).
- Enabled via `FactInserter.EnableLLMDedupe()` (best-effort; no-op if OPEN_AI env absent, so tests
  stay deterministic on the embedding-only path). Wired into news, twitter, frpolitics, ingester, and
  the `dm plugin news` / `dm ingest files` CLIs. Golden: `TestFactDedupeAdjudicatorGolden` (local-only).

## Translation repair, public min-facts floor, complementary-cause rule (2026-06-06)

- **English-translation gap fixed.** ~10-24% of non-English facts had a NULL `fact_text_en`
  because the extraction LLM silently dropped `normalized_fact_en` (parser warned + inserted anyway,
  no retry/backfill). Now: (1) extraction prompt makes English MANDATORY for non-English facts;
  (2) new `core.Translator` (translate-only LLM call) + `dm facts backfill-translations` CLI +
  `storage.GetFactsMissingEnglish`/`SetFactEnglish`; (3) a daily-pipeline repair step fills any gap
  each run (self-healing — serves as the retry). Run the CLI once to clear the existing backlog.
- **Public site shows only entities with >3 facts** (`publicMinFacts=4` floor in `handleGetPersons`).
  Hides one-off mentions and junk entities (institutions, a bookstore, mis-extractions) the pipeline
  incidentally creates — ~416 persons shown vs ~1576 before. Considered queue-only (24) — too tight.
- **Contradiction detection: complementary causes are not contradictions.** Extended the
  `COMPLEMENTARY FACTS` negative rule in `promptrules/contradiction.go` to cover two compatible
  explanations/causes of the same problem ("it's bad organization" + "it's not a resource problem").
  Fixes the #156 (Darmanin) FP class at the detection stage; the verifier golden also covers it.

## Contradiction-pass dedup net + DEDUPE merge (2026-06-06)

- The contradiction detector now doubles as a dedup net. When the LLM judge concludes a near-identical
  pair (KNN cosine sim >= 0.80) is NOT a contradiction, it asks `FactDedupeAdjudicator.IsSameClaim`;
  if they are the same claim restated, `storage.MarkFactDuplicate` records a reversible 'duplicate'
  `fact_relations` row (lower id kept canonical, higher id hidden from active queries / future
  detection). The embedding is stance-blind by design, so the judge — not the vector — is the
  authority on contradict/agree/dedupe. Best-effort + gated on similarity to bound LLM calls; skipped
  under --dry-run. This is a second net behind ingestion-time tri-band dedup, catching pre-existing
  duplicates the contradiction pass encounters.

## DEDUPE retro-repoint of contradictions (2026-06-06)

- When a fact is marked a duplicate, contradictions it already anchored keep referencing it and keep
  counting toward the score (getPersonFactsCount/contradiction stats don't exclude duplicates).
  `storage.RepointFactDuplicate` moves those contradictions onto the canonical fact — dropping
  fact-vs-own-duplicate self-pairs and pairs that become redundant — then recomputes affected scores.
  Stance memberships of the hidden fact are left intact (preserves every-fact-has-a-stance; it's
  already excluded from active queries). Exposed as `dm facts repoint-duplicates [--apply]` (read-only
  by default) and run as a daily-pipeline step right after contradiction detection.

## Extraction precision: enforce poll + passive-event rejection (2026-06-07)

- The 2026-06-07 run extracted 6 poll-statistic facts (approval/confidence/ranking %s attributed to
  the politician as their own CLAIMED/ANNOUNCED/CONFIRMED statement — 4 literally said "selon le
  sondage") and a few passive non-statements ("X invited to a banquet", "appeal outcome will be known
  on [date]"). These violated EXISTING reject rules (7 polls, 3 scheduling, 4 logistics) — an
  enforcement gap, not a design gap. Poll snapshots also manufacture numeric false-contradictions.
- Strengthened the extraction prompt (`core/fact_extractor.go`): rule 7 promoted from a one-liner to
  emphatic + examples (approval/popularity/confidence/ranking %s, "selon le sondage", "% des
  Français"); rule 4 now names passive events (invitations, scheduled dates, "will be known");
  Self-Consistency Check gained a final poll/passive gate. Extended the extraction golden test
  (`TestRemoteLLMExtractor_ExtractFacts`) with poll + passive content asserted rejected — passes
  (model now drops them). Design unchanged: keep the entity's own statements/actions + definitive
  outcomes; reject poll snapshots (they're not the person's contradictable claim).

## Codebase /simplify cleanup pass (2026-06-08)

- Whole-codebase reuse/simplification sweep (no behavior change). Added two shared LLM helpers in
  `backend/llm/parse.go`: `ChatCompletionsURL(base)` (idempotent endpoint builder) and
  `StripJSONCodeFence(s)` (strip ```json…``` wrappers). Replaced ~14 inline `+ "/chat/completions"`
  builders and ~7 divergent fence-strip blocks across core/contradiction/categorizer/complaints/
  entityvalidator/frpolitics/cmd with calls to them. Future rule: never hand-build the chat URL or
  strip code fences inline — use the `llm` helpers.
- Deleted dead code: `backend/core/relation_classifier.go` (whole file — referenced a non-existent
  batch_job.go), unused storage methods `insertFact`/`InsertFactRelation`(singular)/`RecordPairCheck`
  (singular), and three unused frpolitics funcs. Replaced builtin-shadowing `min`/`minInt` helpers
  (go 1.24 has variadic `min`/`max` builtins).
- Consolidated near-identical postgres.go query methods behind private helpers: `loadPairSet`
  (contradiction/checked pairs), `personIDByColumn` (the three GetPersonIDBy* lookups), and
  `queryPersonInfos` (four id/name/entity_type scan loops).
- Deferred (filed as issues, behavior-sensitive / need DB tests): OpenAI env-read consolidation +
  default-model drift (config.go defaults gpt-3.5-turbo/ada-002 but code hardcodes
  gpt-4o-mini/text-embedding-3-small — config.LoadConfig is effectively dead for these); N+1
  per-similar-fact queries on the dedup/ingest hot paths.
## 2026-07-16: In-process LLM failover — LiteLLM proxy removed

Replaced the self-hosted LiteLLM sidecar (a ~1 GB Python service, the largest the host cost) with in-process primary→fallback in the two OpenAI-compatible chokepoints, so chat + embeddings call the home box directly (Cloudflare Access) and fall over to OpenRouter. Batch providers (Gemini/Mistral, direct to their own APIs) are untouched.

- `backend/llm/client.go`: `DoRequest` now tries the primary (caller's URL/key, with CF-Access headers + the existing exponential backoff / credit-error callback / retry metrics) and, on failure, retries a fallback endpoint with the fallback model. `NewBaseLLMClient(component)` reads the fallback/CF config from env — **no new call-site changes** (beyond the `component` label already threaded through), so all ~10 chat clients get failover for free.
- `backend/embedding/openai_embeddings.go`: same pattern; also pins `encoding_format:"float"` on the request (the proxy used to inject it; llama.cpp/OpenRouter reject its absence).
- **New env (all optional; unset ⇒ primary-only, unchanged):** `FALLBACK_URL`, `FALLBACK_KEY`, `FALLBACK_CHAT_MODEL`, `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET`. CF-Access headers go to the primary only; the chat fallback swaps in `FALLBACK_CHAT_MODEL`, embeddings reuse `OPEN_AI_EMBED_MODEL`.
- **Behaviour note:** the existing chat backoff (up to ~2 min on retryable 5xx/timeout) is preserved, so failover to the fallback happens only *after* that backoff. Fine for mirror's batch/pipeline workloads; shorten `createDefaultBackoffPolicy` if faster failover is wanted.

## Self-exempting double standard: a new contradiction class (2026-07-16)

- The judges only recognised a person contradicting their OWN commitments. A politician who demands a
  sanction for a CLASS ("les condamnés devraient être inéligibles à vie", Le Pen 2013) and then exempts
  themselves once they enter that class (convicted, yet running) was silently dismissed as "a general
  policy stance, not a personal commitment" — the verifier filed it under `vague_vs_specific`, the
  auditor under `different_subjects`. This is the sharpest hypocrisy signal the product exists to
  surface, so it is now an explicit YES pattern: SELF-EXEMPTING DOUBLE STANDARD. Guarded by three
  conditions (the person stated the rule themselves; they clearly fall in the class; the self-exemption
  is on the SAME sanction) — do not stretch it to loose "hypocrite" claims.
- Landed in three places, deliberately NOT four: the stance analyzer (`promptrules.ContradictionRules`
  + PAIR 30, the primary catcher), the shared FP catalog (`false_positives.txt` — EXCEPTION clauses on
  `[vague_vs_specific]` and `[different_subjects]`, which is what carries the verifier), and the
  auditor's own positive block. The batch detector (`ContradictionRulesAndExamples`) was left alone:
  its prompt + the 4096-token output reservation already sits ~300 tokens under the local model's 12k
  context, and adding the pattern overflowed it (400 at 12001 tokens). It is a secondary net; the
  stance path catches this class at ingest.
- The rule OVER-FIRED on first contact with prod and had to be re-guarded. The MLP rebuild produced 19
  edges off the 2013 rule, but only ~8 were real: the model happily paired "les condamnés devraient
  etre ineligibles" with the conviction itself, the appeal ruling, the parquet's requisition, and
  awaiting-verdict narration. Those are the class-membership TRIGGER, not the breaker — being
  sanctioned is what her own rule prescribes, so it AGREES with it. Semantic guards ("the other fact
  must be her own claim") did NOT hold at either gate. What worked was a MECHANICAL check keyed on the
  canonical_verb already rendered in the analyze prompt: never emit a self-exemption edge against a
  WAS_CONVICTED key fact or a court/parquet/verdict fact. Measured on the real prod pairs: conviction
  0/4, parquet 0/4, awaiting-verdict 0/4, her own eligibility claim 4/4. Rule of thumb: when a small
  local model won't hold a semantic distinction, look for a mechanical one already in the prompt.
- Layering lesson: the verifier CANNOT be made to drop the rule-vs-conviction shape (0/4 even with an
  explicit guard — its own REASONING says she "was subject to that very sanction" and it still votes
  keep, same stubbornness as the Attal #169 homonym). Fixing it at the ANALYZE step is what mattered,
  because that is the gate that CREATES the edge; the verifier never sees a pair the analyzer refuses
  to propose. Prefer fixing over-firing at the layer that emits, not the layer that reviews.
- MEASURED TRADE-OFF, do not "optimise" it away: the auditor's SELF-EXEMPTING bullet must stay VERBOSE.
  Compressing it to one line recovers the FP drop-side (FP_177 1/5 -> 4/5) but collapses TP_330b to 0/5
  — i.e. the auditor starts evicting the very contradiction this change exists to protect. Keeping it
  verbose holds TP_330b at 5/5 and costs FP_92 + FP_177 (5/5 -> 1/5) on the AUDITOR ONLY. Accepted
  because (a) the audit test's own contract says never auto-delete a real contradiction is the critical
  direction, (b) the auditor is read-only in the daily pipeline, and (c) the VERIFIER still drops both
  FP shapes at insertion, so they never enter the DB — the auditor's drop side only matters for
  retroactive `contradictions check --apply` cleanup. Net vs baseline: audit keep 6/9 -> 8/9, drop
  19/19 -> 18/20.
- Tried and rejected: giving FP_92/FP_177 their OWN "remove decisively" block in the auditor prompt
  (to counterbalance the self-exemption keep-pressure without shrinking that bullet). It recovered
  FP_177 5/5 but shifted the whole auditor toward remove, collapsing TP_103 5/5 -> 0/5 and TP_330b
  5/5 -> 1/5. Reverted. Conclusion (proven from BOTH directions — shrink-keep and add-remove): the
  auditor gate is at this small local model's capacity ceiling; any counterweight to recover the two
  FP drops breaks a genuine keep case. Do not re-attempt on this model. The two FP drops are the
  accepted, documented cost; the verifier still blocks both at insertion.
- Prompt-tuning lesson (repeat of the gh#23 finding): adding a verbose YES pattern to a refute/audit
  prompt shifts the model's global prior toward "keep" and silently breaks unrelated FP buckets — a
  full YES bullet in `verify.go` regressed FP_245 + FP_92 from 4/4 drop to 0/6, and a 4-line worked
  example in the auditor cost FP_92 + FP_177. Both were fixed by expressing the rule as a narrowly
  scoped EXCEPTION on the specific FP bucket (catalog) plus a ONE-LINE example. Prefer scoped
  exceptions over new positive bullets; measure FP cases in isolation (temp 0.1 makes single full-suite
  runs a misleading signal — stress-test 3-6x).

## Judge-map docs + analyze-path golden harness (2026-07-17)

- Corrected two dangerously stale docs: pipeline step 7 claimed the contradiction auditor is
  "READ-ONLY today" but it has run with Apply:true (auto-evict) — deletions are gated by THREE
  signals (audit remove + confirming 2nd audit pass + verifier cross-veto, see applyAuditRemoval);
  and promptrules/contradiction.go claimed ContradictionRules is embedded by the detector and
  auditor — it is consumed ONLY by the stance analyzer (they embed their own
  ContradictionRulesAndExamples). Consequence worth remembering: tuning ContradictionRules is
  isolated to the analyze judge; tuning false_positives.txt hits every judge. A judge map now
  lives at the top of backend/contradiction/testdata/verify_cases.yaml.
- NEW: TestAnalyzeGolden (backend/core/testdata/analyze_cases.yaml) — first golden coverage for
  the ANALYZE judge, the one that CREATES contradiction edges (previously all its probes were
  throwaway files). Edge-level assertions (must_flag / must_not_flag per key-fact ID), verdict
  optional. It found a live bug on its first run: the self-exemption over-fire returns in the
  REVERSE direction (conviction arrives as the NEW fact vs a stored normative rule).
- That reverse direction is now a DOCUMENTED KNOWN LIMITATION, not a fixed bug. Three fixes were
  measured and every one broke case D (denial-vs-conviction, the core signal): direction-symmetric
  mechanical check (C 0/3, D 3/3), a global "NORM vs FACT" typing rule (C 1/3, D fail), a worked
  PAIR-31 NO example (C 1/3, D 0/3). The C↔D seesaw is the same fixed-capacity behavior measured
  on the verifier and auditor — now confirmed on all three prompt-tunable judges. D wins; the
  residual FP shape is caught via `dm contradictions dismiss` / audit-prod-data.
- Prompt-anatomy findings for a future consolidation (do NOT attempt on the current model without
  a much bigger analyze golden): ContradictionRules is 4,454 words of which 66% is 30 worked
  examples; its NEGATIVE PATTERNS section is a hand-maintained parallel of the shared 23-bucket
  false_positives.txt and has already drifted (the vague_vs_specific self-exemption EXCEPTION
  exists only in the catalog). The abstract-principle refactor (NORM vs FACT) failed on this
  model — it obeys concrete examples, not typing rules — so consolidation must wait for a model
  that holds abstractions, and would then let the analyze judge embed the shared catalog directly.

## Auditor example diet (2026-07-17, follow-up)

- The auditor now audits with a CURATED subset of the shared worked examples (10 of 28) instead of
  the full set: `auditorKeptExamples` + `leanAuditRules()` in auditor.go filter the tail of
  `ContradictionRulesAndExamples` at init — single source, no text duplication, batch detector
  untouched (locked by TestLeanAuditRules). Assembled audit prompt: ~5.4K → ~3.7K words. (Note: the
  earlier "10K words / 6.6x verifier" figure in this file double-counted the embedded body; true
  ratio was ~3.6x.)
- Measured outcome (x5 per case): FP_177 1/5 → 5/5 (the bloat hypothesis was right for it),
  full-suite drop 18/20 → 19/20, TP_330b held 5/5, TP_114 0/5 and FP_92 ~0/5 unchanged (their
  failures are model-intrinsic, not bloat), TP_103 5/5 → 3/5 (real cost, confirmed x10).
- Why the trade was accepted: the degraded direction (auditor keeping TP_103) is double-protected
  in prod — a deletion still needs the confirming 2nd pass AND the verifier cross-veto, and the
  verifier keeps TP_103 reliably, so 3/5 only means occasional "unstable" log lines. The improved
  direction (evicting FP_177-shaped stored junk) had NO other cleanup path. Net: prompt bloat was
  ONE real cause of auditor failures (proven), alongside the model-intrinsic ones (TP_114, FP_92).

## Extraction attribution/grounding rules + post-audit cleanup (2026-07-21)

- The 2026-07-21 prod audit (workflow: 36 suspected, 19 adversarially confirmed) found the extractor's
  dominant defects are invented specifics and misattribution, so fact_extractor.go gained three
  self-consistency rules: ATTRIBUTION (aides/"bras droit" statements are not the principal's; @handles
  resolve only to the person they name, never a substituted guess — prod bug turned "@eciotti" into
  "Christian Estrosi"), QUALIFIERS (preserve "en partie"/"notamment" — dropping them manufactures
  absolutes), EVENT DATE ≠ PUBLISH DATE (prod bug dated a July 14 speech July 15 from the article
  timestamp). Locked by TestRemoteLLMExtractor_AttributionGrounding; existing extraction goldens pass.
- Ops lesson recorded: the July 19 auto-audit ran the OLD binary (the host auto-deploy silently broken —
  deploys via `host up` until the dashboard trigger is fixed) and deleted 7 of 8 self-exemption
  edges while keeping 11 FPs the workflow confirmed. Dismissed the 11; full stance rebuild with the
  new prompts relaunched to restore + generalize.

## Reliability trio: LLM circuit breaker, run-start rows, pipeline watchdog (2026-07-24)

- The Jul 22-24 box outage exposed three silent-failure modes (mirror-s8t/bp0/1ir): every LLM call
  burned the full ~2min primary retry before falling back (fallback WORKED but was 30x too slow —
  runs ballooned 10h+ and died), killed runs left no pipeline_runs row at all, and nobody was told.
- Circuit breaker (backend/llm/breaker.go): process-wide primary-down state shared by chat AND
  embeddings (same physical box). Armed by a hard primary failure or the new start-of-run probe
  (llm.ProbePrimary in runDailyPipeline); while armed, calls skip the primary entirely and go
  straight to the fallback; half-open retry every 10min auto-recovers. OpenRouter serves
  qwen/qwen3-embedding-8b (verified empirically), so a fully box-less run is now viable.
- pipeline_runs rows are now INSERTed at run START (status='running', finished_at nullable via
  migration 20260724120000) and completed by UPDATE — killed/hung runs leave a visible corpse.
- Pipeline watchdog (backend/alerting/pipeline_watchdog.go, started by dm web serve): hourly health
  check, Telegram alert on >30h without a successful run / runs stuck 'running' >6h / errored runs,
  24h per-condition cooldown. Gotcha for future tests: the breaker is process-global — any test that
  exercises a primary failure must resetBreaker() or it poisons later tests in the package.

## Chunker actually chunks + breaker ignores 4xx (2026-08-30)

- `util.ChunkText` split only on `\n\n` and its accumulation loop closes a chunk only when it
  already holds something, so a paragraph longer than the budget was emitted WHOLE. News live
  blogs arrive as one unbroken block: 13 of 29 chunked articles in the 08-30 run came back as
  `chunk_count=1` at 2-3x the computed budget, and the densest one overflowed the local model's
  12k context. The dynamic sizer and `approxCharsPerToken=3` were never the problem — 3 is
  deliberately conservative (measured 4.37 actual), and the budget it computed was correct.
  `splitOversized` now breaks over-long units at sentence ends, then word boundaries, then a
  rune-safe hard cut. Invariant locked by TestChunkTextNeverExceedsBudget: no chunk may exceed
  the caller's budget, whatever the input shape. The old test ASSERTED the broken behavior
  (`SingleLongParagraph` expected a 250-char paragraph back whole at chunkSize 100) — that is why
  it survived so long.
- The LLM circuit breaker armed on ANY primary failure including 4xx. One oversized prompt
  therefore declared the home box down for 10 minutes and pushed every other call onto the
  fallback; with OpenRouter at zero credits this cost the 08-30 run its whole second half (811
  x 402, last fact stored at the exact minute the breaker tripped). 4xx is now a typed
  `llm.ClientError` and `isEndpointOutage` keeps it from arming the breaker — the call itself
  still fails over, it just no longer speaks for the endpoint. 5xx/transport/sustained-429
  still arm it.
- Ops lesson: nine consecutive `no_content` runs (08-18..08-26, zero facts each) raised no alert,
  because the watchdog counts `no_content` as a successful run. See mirror-07b.

## Embeddings retries 5xx + contradiction reply budget measured, not guessed (2026-08-31)

- The 08-31 cron lost its second half to a 502 on ONE embeddings call. The box was healthy
  (llama-swap served 200s throughout); cloudflared showed the real cause — a ~1s QUIC tunnel
  reconnect, `timeout: no recent network activity ... dest=.../v1/embeddings`. sendEmbedOnce
  retried ONLY 429, so a 5xx or a dropped connection returned instantly and armed the
  PROCESS-WIDE breaker (chat + embeddings share the box) for 10min → 870 x 402 on the empty
  OpenRouter fallback, facts down to 24. It now retries 5xx AND transport errors with the same
  jittered backoff the chat path has always used, so only a SUSTAINED failure speaks for the
  endpoint. Tests cover all three arms: transient 5xx absorbed, sustained 5xx still surfaces
  (a dead box must still arm the breaker), 4xx never retried.
- contradictionMaxTokens was 4096 "for Gemma's thinking mode" — but AreContradictoryBatch, the
  only user of the constant, explicitly DISABLES thinking, so that reservation guarded a mode
  the path never enters. max_tokens is charged against the context BEFORE generation, so it was
  costing prompt budget for nothing: 7770 (prompt) + 4096 = 11866 of 12000, a 1.1% margin, and
  longer pairs 400'd. Added a "contradiction token usage" log line (prompt/completion/total,
  unused_reservation, WARN on finish_reason=="length") and MEASURED: replies are 75-135 tokens,
  never truncated. Set to 1024 (~8x observed max) → margin 26.7%, the previously-400ing pair now
  passes, all 17 golden verdicts unchanged. Keep the log line: it makes the next calibration a
  lookup instead of a guess.
- Method note for future context-overflow bugs: three distinct sites have now hit the same 12k
  wall (chunker, stance metadata, detector reservation). Before touching --max-model-len, check
  which side is at fault — the box has ~1.8GB VRAM free with 8GB already CPU-offloaded, so
  raising context is not the cheap lever it looks like.

## Judges vote 3x, goldens fixed, and a red suite is never pushed (2026-09-01)

- Rule adopted: never push while ANY test fails, pre-existing or not. Proving "not mine" by
  stashing is necessary but not sufficient — fix it. A red suite everyone ignores hides the
  next real regression, and this repo's live-LLM goldens skip in CI so local red is the only
  signal they ever give.
- The audit/verify judges are NOT deterministic at temperature 0: vLLM continuous batching makes
  a verdict depend on what else is in flight, so full-suite runs failed a DIFFERENT random 3-4
  cases each time while those same cases passed 5/5 alone. `JudgeVotes` (contradiction/vote.go)
  takes a majority over N samples with early exit (2 agreeing votes settle it). It lives in
  PRODUCTION code, not the test harness: prod took single-sample verdicts and an unlucky sample
  auto-evicts a real contradiction. JudgeVotes=3 in prod (was 1), after the goldens were green under 3 votes.
- Five stable golden failures fixed by prompt work, with the seesaw measured each round: the
  auditor's example catalog had 3 keep-side vs 7 remove-side examples and NO worked example for
  the three failing keep families (PAIR 33 denounce-then-ally, 34 self-exempting, 35 walk-back
  vs same_direction). Adding them fixed keep 7/10→10/10 but leaked drop 19→17/20; a STEP 0
  "resolve before comparing" (unwind negations, carry numbers, resolve vocabulary like
  imprescriptibilité, resolve who a class rule binds) recovered drop 20/20 without losing keep.
  Final: audit 30/30, verify 29/29, three passes each. Lesson: the failing excuses were ALREADY
  prohibited verbatim in the prose rules — the model ignores prohibitions and copies examples.
- contradictionMaxTokens 4096→1024 (measured 75-135 used) and two more 16384 reservations
  (frpolitics summarizer, core dedup) that exceeded the whole 12k window and 400'd on every call.
  Four distinct sites hit the same wall this week; check the reservation before the model.
- frpolitics party facts were ZERO since May: the per-legislature Organes.json.zip URL 404s and
  loadOrganeCacheForLegislature swallowed it into an empty map, so every party name resolved to
  "" and the seeds fell back to raw refs — prod holds 13 "parties" named po840056-style.
  Organes now come from the AMO30 archive the actor loader already used; an empty cache is an
  error; unresolved refs are never seeded. This was a textbook silent bypass. The e2e verifier
  then scored 0% because it keyed deputy votes with NormalizeString (accents kept) while persons
  are stored under NormalizePersonName (accent-free): "apparentés" never matched "apparentes".
  Both e2e tests skipIfCI, which is how this stayed red locally for three months unnoticed.
- Extractor: ATTRIBUTION rule tightened (drop unnamed speakers, resolve @handles only from the
  handle, never world knowledge) — but the first wording leaked into scope (named non-political
  speakers kept); tying it to the scope rule fixed both, 6/6 each.
- The extractor scope test (Deschamps/Jimin must be rejected) slipped ~1 run in 10 on a single
  sample; prod is guarded downstream by the entity validator's R10, so the test now judges the
  rule by majority of 3 extractions — the same policy as the judges. `make test` runs packages
  with -p 1: six packages hitting vLLM at once flips verdicts even under 3 votes (mirror-gka).
- Storage: TestPersonSimilarity inserted fixture persons through the live validator (Wikipedia +
  Serper + LLM each) — hung past 10m and burned credits; SetEntityValidator(nil) like the other
  tests. The stance-invariant collector goroutine outlived Close() (spammed "closed pool");
  Close now cancels it.

## Vote facts attribute to EVERY voter + French vote statements (2026-09-05)

- A vote fact is ONE row shared by everyone who cast that vote (text-dedup by design), but only
  the arbitrary FIRST record got a fact_mentions row; the other voters were filed as
  InvolvedPeople, which by the extractor's own rule does NOT attribute. Net effect in prod:
  deputies owned ZERO vote facts, party attribution was first-come, and "said X, voted Y" —
  the point of ingesting votes — could never fire. New StoreFactInput.CoActorIDs: pre-resolved
  co-actors get real mention rows in the same LinkPersonsToSourceMention call.
- CONSEQUENCES (traced, deliberate — see the CoActorIDs doc comment):
  * contradiction detection is mention-scoped, so each voter's statements now pair against
    their votes; expect vote-vs-statement contradictions to start appearing and scores to move.
  * fact_count / honesty denominators grow for every voter (bulk RefreshPersonFactCounts, one
    statement, after each law batch); scores recompute lazily from live mention counts.
  * stance routing still runs ONLY for the subject person — stances are per-person documents,
    so co-actors' stance systems never see the shared fact; their contradiction coverage comes
    from the nightly pairwise detector, not the stance/analyze path.
  * e2e locks the fan-out: vote sources must average >1 mention and deputies must own facts.
- Vote statements are now French ("a voté POUR le texte de loi : « ... »") and the law
  summarizer is instructed to emit French short_title/simple_statement — texts were English
  with broken grammar ("collectively voted FOR a Creates a stable...") while labelled lang=fr.
  The e2e position parser and fixtures follow the French forms.

## Amendment-vote semantics: classify the object, fetch the amendment, never invert a ballot (2026-09-05)

- The system's first said-X-voted-Y ("Panot voted against nationalisation") was FALSE: scrutin
  17-4373 voted a UDR sous-amendement stretching a Cour-des-comptes audit from 6 months to a
  year; every non-RN group rejected it. Worse, scrutin 4375 shows LFI also voted against the
  6-month audit itself — proving CONTRE(modification) NEVER implies POUR(status quo): both
  ballots express one position ("no dilatory audit at all") that neither states. A vote judges
  its OBJECT, and an amendment ballot's object is the modification, not the parent bill.
- Tier 0 (votetype.go): deterministic scrutin classifier from the formulaic titles — ensemble/
  article/motion/declaration are TEXT-LEVEL (the ballot IS the position); amendement/
  sous-amendement are modification-level. Zero LLM.
- Tier 1 (amendements.go): the AN Amendements archive (~300MB/legislature, flaky transfers —
  resume with curl -C - style retries; cached as asset/cache/Amendements_<leg>.json.zip) gives
  dispositif + exposé sommaire + author group per amendment; filenames encode texte+numero so
  lookup scans the central directory only, disambiguated by sort-date = scrutin date, AMBIGUOUS
  → nil. Vote fact texts now describe the voted object: "a voté CONTRE le sous-amendement
  n° 372 (UDR) visant à accorder à la Cour des comptes un délai suffisant..., déposé sur le
  texte de loi : « ... »". Unresolvable amendment content GATES the scrutin (no facts): a fact
  that misstates the voted object is worse than no fact.
- Judge rules, all four judges: [relative_vote_preference] in the shared FP catalog (amendment
  ballots only; forbids reading CONTRE-the-modification as anti-bill or pro-status-quo; keeps
  the ballot cast AGAINST the very measure the person demanded), a scoped line in the analyze
  rules (first version said "only a vote can break a promise" and broke denial-vs-conviction —
  quantifier scope matters), PAIR 36 worked example, and 8 golden cases across verify/audit/
  analyze (incl. the real 4373/4375 pair).
- Tuning lessons re-confirmed the hard way: catalog GROWTH re-destabilizes boundary cases
  (FP_170/FP_selfexempt/TP_330b wobbled from added prose; fixed by SHRINKING the bucket 40% and
  deleting a redundant example); TP_103 vs TP_330b are anti-correlated through prompt mass —
  a per-example trap-check fixed one and broke the other; the stable fix was ONE symmetric
  SELF-CHECK paragraph in the audit prompt covering both forbidden excuses. All four judges now vote:
  llm.MajorityVote is shared; AnalyzeVotes=3 (judge #1 creates edges — keyed on verdict +
  duplicate + exact edge set), JudgeVotes=3 (verifier), AuditVotes=5 (runs every 5 days, a
  wrong eviction is destructive, extra votes are free). The audit prompt gained a symmetric
  SELF-CHECK listing the three forbidden excuses that kept trading places under prompt mass
  (denounce-then-ally "pas incompatible", self-exempting "sujet différent", numeric walk-back
  "même direction" with two different numbers) — one paragraph stabilized all three where
  per-example trap sentences see-sawed. D_denial fixed by a worked example on
  CLAIM↔AUTHORITATIVE FINDING plus the analyze vote. Goldens run the PROD vote counts (no
  test-side overrides), so they measure exactly what prod does.

## Stance fan-out: votes enter every voter's stance system (2026-09-11)

- mirror-py3 implemented. A shared vote fact is stance-routed only for its subject at insert;
  a fact may now be a member of MANY stances — but at most ONE per (person, fact). The nightly
  pipeline gained Step 3b "stance fan-out": the person-scoped orphan queries derive the pending
  (person, fact) pairs straight from the DB (no queue table — idempotent, self-healing,
  retroactive), and `dm facts assign-stances` (now with --limit) routes them through the live
  ProcessNewFact path, 250/night, before the merge sweep. ProcessNewFact needed zero changes:
  every write it does was already (person, stance)-scoped.
- The enabling audit found 13 code sites that ASSUMED one-stance-per-fact globally; the
  dangerous pattern is any fact→stance lookup not scoped by person_id. Fixed: GetFactStanceID/
  GetFactStanceMap take personID; RecordContradiction + BundleBackfillFastPath shared-stance
  CTEs join on the contradiction's person (a foreign bundle_stance_id silently detaches the row
  from the owner's per-stance breakdown); GetPersonContradictions per-fact stance joins became
  person-scoped LATERALs (unscoped, they multiplied API rows k× per extra membership and broke
  pagination); GetStanceContradictionGroups filters by the stance owner's person; the two
  orphan queries define "orphan" per (person, fact) — this scoping IS the feature; MergePersons
  deduplicates cross-label memberships post-merge (keep oldest).
- Folding is now REFUSED for shared facts, both directions (new fact with CoActorIDs; matched
  fact with >1 mention persons; same guard in `dm facts fold` clustering): fact_relations
  'duplicate' is a GLOBAL flag, so folding a shared vote fact under one person's meta would
  silently remove it from every other voter's active set. Residual sibling hazard filed as
  mirror-eqm: the non-fold DROP dedupe path can still discard a new shared fact.
- New gauge stance_routings_pending_total (the fan-out backlog, per (person, fact)); the old
  facts_without_stance_total keeps meaning "no stance anywhere" (hard invariant, stays 0).
- Detector prompt: TEMPORAL rule 3 (same-date default NO) contradicted the HARD GATE (an
  authoritative finding substitutes for the second utterance); rule 3 now names the
  claim-vs-authoritative-finding alternative. Found via a logprob probe of the flaky Live
  case "Direct Negation": the fork was mid-REASONING ("entreprise" 0.39 vs "intéressé" 0.35 —
  who is "the person" when the subject is the anonymous "The CEO"), not at the verdict token,
  and the verdict then followed the vLLM prefix-cache state. Lessons: (1) at temperature 0
  three votes in the same cache state return the same answer — voting does not protect a
  judge against this failure mode; (2) an LLM's uncertainty lives in its reasoning tokens, a
  verdict-token confidence would have missed the case; (3) golden pairs must name the subject
  like every prod fact does — an anonymous role tests something the system never does.

## Stance metadata refresh is incremental (2026-09-11)

- mirror-t1z. The label + summary refresh used to inline EVERY fact of the stance on every
  add — O(n) per fact, O(n²) over a stance's life, and past ~200 facts it overflowed the 12k
  context and the stance silently froze forever (the biggest topics, exactly the ones that
  matter). Now: the live path folds ONE new fact into the current label + summary
  (~3k tokens, O(1) in stance size), and the tensions the analyze just verified are passed
  in explicitly ("⚠ CONTRADICTS the earlier fact …") instead of hoping the LLM rediscovers
  them among 200 lines. New column stance_documents.summarized_at is the cursor: memberships
  added after it are what the generic refresh (merge sweep, deferred backfill, CLI) folds in,
  chunked to the context; nothing new = no LLM call. Merge copies memberships with a fresh
  added_at so absorbed facts count as new to the destination.
- The full rebuild survives as COMPACTION: every 10th fact on the live path, and on every
  generic refresh, when the whole fact list fits the context — which is 99.6% of prod
  stances (median 1 fact, 24/6249 over 30, 9 over 60). Small stances get a free drift reset;
  the big ones stay incremental and never become un-refreshable. No map-reduce needed.
- `dm stances refresh-metadata --full` rebuilds from every fact (rolling fold when too big).
- Design note: the creation placeholder summary IS the first fact's text, so the second
  fact folds onto a correct seed; a never-summarized stance folds from scratch.

## AN débats: séance transcripts become first-person facts (2026-09-15)

- mirror-2xw. New source: the Assemblée's daily Syceron export (one zip, whole legislature,
  one XML per séance). Every paragraph carries the speaker's AN actor id — the same id as
  persons.attributes.assemblee_id — so attribution is a join, never a name match; the
  ordre-du-jour point names the texte (n°), and art/adt/ssadt name the article/amendement
  under discussion (the structural link to the votes). Permalinks are rebuilt from the uid
  (session kind + parliamentary year) and numSeanceJour/dateSeanceJour, with the paragraph
  id as anchor — verified live for every session kind.
- Unit of extraction = one document per (speaker, séance): the speaker's substantive
  paragraphs (≥300 chars, not the chair, not heckles) under a header naming the sitting and
  the texte, with a bracketed marker before each change of article/amendement (a rapporteur
  gives forty "avis" on forty amendments in one séance — one header would mis-attribute
  thirty-nine). 70% fewer LLM calls than one per intervention and better context. The shared extractor runs with transcript instructions ("every sentence is the
  target's own words"); facts whose subject is not the speaker are dropped (quotes of
  third parties); the official record is the verbatim quote; assemblee_debat is a
  quote-required source type. Insertion goes through FactInserter like news, so dedupe,
  stance routing and analyze apply — "said X in the hemicycle, voted Y" fires live since
  votes sit in every voter's stances (mirror-py3).
- Idempotence: debate_documents records every processed document, zero-fact ones included,
  keyed by (cr_uid, actor_id) — NOT by the permalink, whose paragraph anchor moves when the
  substantive threshold changes or the AN republishes a séance (all 601 files are still
  "avant_JO"; the JO republish will hit the whole archive at once). Pending = archive
  documents not in that table. MergePersons repoints the rows. Pipeline: Step 3c daily feed (séances of
  the last 4 days — the archive lags a day) before merge/detection; Step 9 (last) backfill
  of the 2025-2026 session, newest first, until 11:00 Europe/Paris (wall-clock deadline
  instead of a count: the user chose to let the night run long). `dm plugin frpolitics
  --mode debats --since --max-docs --newest-first` drives manual chunks.
- Backfill priority (2026-09-16, first night's lesson): the backfill window is ~1 h once the
  other steps have run (re-judge + detection took 2h45), i.e. 20-35 documents a night against
  a 9,669 backlog. Documents are now ordered by the speaker's press presence (active
  news/twitter facts, most first) then newest séance — the window is spent where transcript
  facts can meet press facts; speakers with no press facts come last. The daily feed stays
  unprioritized (everyone). Capacity itself is still the constraint: ~2.8 min per document
  (extraction + ~36 s of stance routing per fact); a daytime job would be the real fix.
- Run hygiene: a streak of 10 failing documents aborts the run (outage), an all-failed run
  is a step error, facts whose subject is not the speaker are counted per document in
  debate_documents.facts_dropped (cron logs are not retrievable after the fact, so the table
  is the durable channel; product question left open: the speaker's group as subject —
  "notre groupe demande…"). First prod document (the host container, 2026-09-15): 96 s,
  2 facts kept, 1 dropped; the same document re-extracted locally kept 3 — the extractor's
  subject choice is nondeterministic on borderline facts.
- Measured on the full archive: 601 séances, 60k substantive tracked interventions
  (74.6M chars); 2025-2026 session = 9,669 documents; ~65 documents per sitting day.

## Negative pair verdicts expire with the rulebook (2026-09-15)

- A NO in fact_pair_checks used to be final forever; only YESes were re-examined (the
  auditor pins each contradiction with a hash of its inputs INCLUDING the prompt, so a prompt
  change re-audits every YES). Now every pair verdict stores judge_hash = hash(detector
  prompt + refutation prompt + vote count); the nightly pipeline re-opens the oldest negatives
  whose hash is not the current one (1500/night, right before detection) so a rulebook change
  re-judges the whole backlog of NOs within days, and does nothing otherwise. NULL hashes (the
  ~9.5k verdicts judged before the column existed) are re-judged once — the "recheck
  everything since the first facts" the user asked for. Replaces the one-off date-cutoff
  re-baseline. Limit that remains: pairs never within KNN reach are never judged at all.

## Generic fact-pair queue + same-séance pairing (2026-09-15)

- fact_pair_queue: any producer can ask the detector to judge (person, fact A, fact B)
  regardless of embedding distance. The nightly detection CONSUMES queued pairs (taken and
  deleted atomically, 300 per person per night, oldest first) into the normal per-person flow
  (dedup, already-checked filter, judge, fact_pair_checks); queued pairs bypass the
  external-verb filter — the producer vouches for them. Transient by design: a pair not
  judged tonight (fact gone, judge error) is re-created by the producer's next idempotent
  run, never leaked. Review lesson: "delete after recording" leaked rows whenever a person's
  pairs were all filtered before judging; consume-on-take has no such path.
- Same-claim facts now keep their provenance: when the inserter drops a restatement (tier-1
  auto-dedupe or the LLM same-claim judge, drop mode), the new source and its mentions are
  attached to the existing fact. The claim was known; where it was said again is new — and
  "repeated the position in séance S2, then voted the other way in S2" only pairs if S2's
  source reaches the surviving fact. Folded members resolve to their meta in the producer.
- First producer (frpolitics): the Assemblée séance id. scrutin.seanceRef and the transcript's
  <seanceRef> are the SAME identifier (verified: same ref → same day), so both vote sources and
  transcript sources now carry metadata seance_ref, and one set-based SQL
  (EnqueueFactPairsBySharedSourceKey) queues every (what they said in the sitting, how they
  voted in the sitting) pair per person — no name matching, no amendement resolution needed,
  ensemble/article votes included. Older vote sources get the key via
  `dm plugin frpolitics --mode backfill-seance-ref` (Scrutins archive, law_key → seanceRef).
- Why a queue rather than a second candidate source inside the detector: the pairing rule is
  provenance-specific (frpolitics knows what a séance is), the judging is not; the queue keeps
  the detector generic and lets future producers (news mentioning a scrutin, …) plug in.

## Prompt layout: static rules first, per-call data next, response format last (2026-09-16)

- vLLM prefix caching reuses the KV cache of a byte-identical prompt prefix, so a prompt that
  opens with the ~8k-token rulebook and ends with the per-call data is prefilled once per
  process; one that opens with the data re-prefills the rulebook on every call. The four
  stance-routing prompts (analyze, topic assignment, full rebuild, incremental fold) were
  data-first; the detector, verifier and auditor were already rules-first. Measured on the
  7-case analyze golden: prefill 29.5 s → 7.9 s, cache-hit rate 58% → 88%.
- Where the time really goes (lifetime vLLM counters, ~250M prompt tokens): decode 81%,
  prefill 19%, ~27 tok/s decode. Output length, not context size, is the cost driver — never
  "pack" several independent facts into one prompt to save context; it buys nothing and
  re-creates the cross-talk the one-verdict-per-call design removed.
- The response-format/JSON block must stay LAST, right before generation. A first layout that
  put the data after the JSON schema produced a malformed-JSON retry and a 76 s runaway
  generation on the same golden; rules → data → format fixed both and needed fewer votes.

## Plugin isolation: phase-hook contract, pipeline registry, plugin-owned tables (2026-09-16)

- The daily pipeline no longer names a plugin. `backend/plugins` defines the contract
  (`Plugin` + optional `Ingester` / `PairProducer` / `Backfiller` phases, `Target`,
  `IngestOptions`, `IngestStats`); `backend/pipeline` holds the one registry and runs each
  phase over it; `cmd/pipeline.go` calls phases, never a plugin. `dm queue process` is the
  ingest phase with a plugin filter and generic options only (per-plugin tuning lives on each
  plugin's own command). Phase order changed once: stance fan-out now runs after ALL ingestion
  (votes and transcripts included) instead of between them — the same-séance pair queue is
  what pairs tonight's speech with tonight's vote, so nothing is lost.
- `backend/deps` builds the shared env-configured components once (extractor, embedder,
  stance manager, scrapers, searchers, object store) and hands them to plugins and commands;
  the six hand-copied bootstrap blocks are gone. Rule: never `NewXFromEnv()` in a plugin or
  command — take it from `deps`.
- Plugin-owned tables are declared by the plugin: `storage.RegisterPersonTable` (repoint on
  merge, cleanup, reset on fact deletion) replaces the twitter/news/débats table names that
  were hard-coded in `MergePersons` and `DeleteFactsForPerson`. Each plugin keeps its own
  SQL in `store.go`; storage keeps only generic tables. The orphan `rss_feeds` repoint is
  gone (no code reads or writes that table).
- frpolitics lost its news mode (`runNewsSearch*`, the news fields of `Options`, the
  `--news-*` flags, the `plugin_fr_politics` binary): the news plugin owns news, and that
  fallback was the source of the 2026-06 full-table sweep. The twitter handle-discovery
  engine moved from `cmd/twitter.go` into the plugin. Débats scheduling constants live in
  the plugin; the run-wide backfill deadline (11:00 Europe/Paris) lives in the pipeline.
- Dead code removed: `backend/ingester`, `backend/plugins/web`, the Google CSE provider, the
  no-op detector, the `SearchProvider` interface and its three `Search` methods, ~20
  zero-caller storage methods, `categorizer` type aliases in `contradiction`, the
  `majorityVote` indirection. `core` no longer imports `entityvalidator` (the rejection error
  is `core.EntityRejectedError`), so the domain layer stopped linking the search providers;
  `backend` no longer imports `extractor`; `contradiction` no longer imports `categorizer`.
- News plugin API: `SearchTarget`/`TargetType` are gone, the provider takes `plugins.Target`
  (`IsParty()`; `organization` now counts as a party like `party`); the provider owns the
  per-target cache-extractor wiring (`Options.CacheDir`/`CacheOnly`, `UseExtractor`) and the
  cache-only ingestion path that used to live in `cmd/news.go`; `dm facts rebuild-from-urls`
  logic is `news.Provider.RebuildFromURLs`. `dm plugin news search --dry-run` now builds the
  full provider (needs the LLM key like the pipeline's dry-run).

## cmd/ is thin: the daily run, detection, audit, sweeps and the API server live in backend (2026-09-16)

- `backend/pipeline.RunDaily(ctx, storer, notifier, RunOptions)` is the daily run; `cmd/pipeline_daily.go`
  is a flag parser. Every generic step is a backend entry point the pipeline calls directly:
  `core.RunStanceFanout` (explicit budget), `core.RunStanceMergeSweep`, `contradiction.RunDetection`,
  `storage.RepointDuplicates`, `contradiction.RunRegroupStep`, `contradiction.RunAudit`,
  `translate.Fill`, then the plugin backfill phase. Nightly budgets are constants in the pipeline
  package. `RunOptions{Smoke: true}` (`dm pipeline daily --smoke`) bounds every phase to a handful
  of items (one target, one item per plugin, one person per sweep, no audit, no backfill) and
  writes no pipeline_runs row: it exercises a deploy end to end in minutes without touching the
  run history the watchdog reads.
- Moved out of cmd, behaviour unchanged: contradiction detection orchestration, the three-signal
  audit eviction gate, reverify, regroup and rebundle (`backend/contradiction`); stance merge,
  backfill/rebuild, fold, orphan assignment, metadata refresh, fact audit, attribution audit,
  person dedupe/unaccent/affiliate, verb backfill, embedding regeneration (`backend/core`, raw SQL
  in new storage files); the HTTP API server (`backend/api`, with an httptest suite it never
  had); balance-alert config (`backend/alerting`); the translation fill and repair
  (`backend/translate`); the entity-validation sweep (`backend/entityvalidator/sweep`, a
  subpackage because storage imports entityvalidator); the scrape worker pool
  (`backend/extractor`); the reset table list (`backend/migrate`). Vector math (dot, cosine,
  L2, weighted centroid) is `backend/util/vector.go`; `util.LooksFrench` replaces the cmd copy.
- Rule going forward: a cmd file holds cobra wiring, flag parsing, name input and printing.
  Anything that loops over persons or facts, builds a prompt, calls an LLM, or carries a
  threshold belongs in a backend package with a test.
- The entity-validation sweep now aborts when the fact counts cannot be loaded instead of
  validating the whole table as zero-facts (that fallback would have spent the search and LLM
  credits the has-facts skip exists to protect).

## Storage is persistence only; one LLM client; one verdict judge (2026-09-16, phase 3)

- Storage no longer runs the entity validator (Wikipedia + Serper + LLM used to run inside a
  person-insert transaction). It keeps a dependency-free `PersonGate` hook; `deps.OpenStorer`
  attaches `deps.PersonGateFromEnv()` and every command opens the storer through it, so every
  production insert path is gated exactly as before while `storage` imports only `core`,
  `metrics` and `util`. Tests open a gate-less storer and no longer need `SetEntityValidator(nil)`.
- Storage's mirror copies of core's DTOs and the 282 lines of field-copying adapters are gone:
  `*PostgresStorer` implements the core storer interfaces directly and returns `core.*` types.
  The honesty-score formula is `core.ComputeHonestyScore` (same constants); storage and the API
  call it. `postgres.go` (4,900 lines) is split by topic: persons, facts, fact_dedup,
  contradictions, scores, stances, search_queue, jobs; `postgres.go` keeps the connection.
- `llm.ConfigFromEnv` / `llm.NewClientFromEnv` / `Client.Chat` replace 16 hand-copied client
  bootstraps and 17 request envelopes (categorizer, detector, auditor, bundler, the core
  dedupe/translator/synthesizer/stance files, entity validator, complaints, translate,
  frpolitics summarizer, twitter discovery). `llm.VerdictJudge` (closed-enum verdict + rule
  parse + one corrective retry) replaces the line-for-line clones in `core/fact_auditor.go` and
  `contradiction/auditor.go`; both auditors' request payloads were captured before and after
  and are byte-identical. `llm.VerifyContradiction` takes a `*llm.Client`.
- The fact-extraction prompt lives in `promptrules` (byte-identical, captured and diffed);
  `categorizer` no longer imports `core`, so an extractor no longer links the whole domain
  layer. The batch content extractor is `extractor.NewBatchExtractorFromEnv`. Quote-required
  source types are registered by each plugin (`core.RequireQuoteForSourceType`) instead of
  being listed in core.
- One deliberate prompt change: the three drifted copies of the stance LABEL RULES are one
  constant (the union); the assignment prompt adds a two-line addendum for what genuinely
  differs (label language, journalist-meta fallback). Assign golden 5/5 three times. The fold
  and rebuild prompts grew by ~500 tokens, so the refresh budgets moved accordingly.
- Dead exports pruned across search, extractor, entityvalidator, alerting, eval, metrics, util
  (14 deleted, 23 unexported). Rule going forward: a symbol is exported only if a caller in
  another package uses it.
- Prometheus gauge collectors (pool stats, facts-without-stance invariant) start with the web
  service (`storer.StartMetricsCollectors` from `api.Serve`), not in the storer constructor:
  only the web process is scraped, and the collector's query racing a test TRUNCATE produced
  a Postgres deadlock that flaked the frpolitics store tests.

## Performance: the merge sweep remembers its answers; hot loops stop asking Postgres per row (2026-09-16, phase 4)

- Measured first: nightly runs take 5-8 h and the time is LLM decode (re-judge + detection
  ~2h45, merge sweep ~1h20, débats backfill ~1h). The one structural waste was the merge
  sweep: it asked the LLM about all 753 persons with two or more stances every night and
  remembered nothing, so unchanged stances got the same question again. Now
  `stance_merge_checks` memoizes each person's swept stance set as an md5 fingerprint
  computed in SQL (ids, labels, summaries) plus the merge rulebook key (hash of the merge
  prompt and the candidate floor); `ListPersonsNeedingMergeSweep` returns only the persons
  whose live fingerprint differs, in one query. A living summary refresh, a new stance, a
  merge or a prompt edit re-lists the person; a person with an LLM failure or a dry run is
  never memoized. Expected effect: the sweep costs proportional to persons whose stances
  changed that day, not to the population.
- Prefix cache: the two dedupe gates (same-position, same-claim; run on every gray-zone
  insert) and the merge decision were data-first and are now rules → data → JSON, like the
  other judges. Verdicts verified unchanged (dedupe golden 4/4 three times; a six-case
  merge table identical before and after). `mergeDecisionPrompt` is the hashable rulebook.
  Unit tests pin the shared byte prefix so a later edit cannot silently break the cache.
- Round trips, per the project rule: the similarity query now carries the canonical verb
  and the mention count (was 1 + 5 lookups per inserted fact); detection's candidate
  pairs come from one statement per person with the most-general ancestor resolved by a
  recursive CTE (was ~16 queries per fact); parent-entity score recompute is one select and
  one update; frpolitics resolves actor and organe ids in two batch lookups per run instead
  of two per mandate event and one name lookup per call instead of per co-voter; person
  name resolution tries an indexed exact match before loading the persons table; the news
  provider builds one fact inserter per call instead of one per fact.
- Storage: one lookup → gate → insert core behind the five person entry points, one folded
  name predicate (both sides folded in SQL, so ł/ß/œ variants match), one person-summary
  projection shared with the photos plugin. Migration `20260916200000` adds the memo table.

## Stance over-fragmentation: assignment golden from prod + a label recall pass (2026-09-17)

- Measured on prod: 73% of stances hold one fact; the weeks of Sep 7 and 14 created ~1,200
  stances, over 90% still singletons. Le Pen got three housing singletons on consecutive
  days next to "politique du logement" (8 facts); Retailleau's "parti girouette" quote got a
  third stance while the RN stance already held the same statement twice.
- Two distinct failures. (1) Decision: the right stance was in the shortlist and the LLM
  still created a new one. The assign golden now carries 17 production cases (shortlists
  reconstructed from the live stance sets); the prompt change is targeted: member facts show
  the candidate's DOMAIN rather than a literal template, a compound fact is routed by its main
  subject and never gets a multi-subject topic, plus worked examples of the failures. 22/22
  three runs in a row. Three cases accept either outcome (one borderline taxonomy, two
  compound facts the extractor should have split). (2) Recall: for 7 of 36 cases the fitting
  stance was not in the top-8 embedding shortlist at all, and centroid ranking did no better
  (ranks 9-47) because both live in the stance-blind topic-slug space. New label pass
  (`pickTopicsByLabel`): for persons with more stances than the shortlist holds, one cheap
  call reads the person's labels and adds up to three picks to the candidates
  (`StanceCandidatesByIDs`). Its golden (`label_pick_cases.yaml`, 9 prod cases): 9/9 three
  runs. Lessons: classify the statement's content, never its venue ("sur BFM" pulled the
  media labels); a principle applied to a policy field belongs to that field's topic.

## Dedupe: folding reaches news, lexical candidates, gate retry (2026-09-17)

- Measured on prod: ~13 clear duplicates among the 224 facts of one night, and stances
  holding the same statement 3-4 times (Faure's "aux côtés des Gilets jaunes" x4). Three
  causes, all fixed. (1) News never folded: the 2026-07-28 wiring landed in a package nothing
  imported, so news (60% of nightly facts) kept dropping under the strict same-claim gate,
  which answers no whenever either side states anything the other does not. The news
  provider now folds with the position gate like twitter. (2) Candidate recall: the
  stance-blind topic slug is nondeterministic across extractions of one statement ("mouvement
  gilets jaunes" vs "mouvement social vie chère", cosine 0.47), so restatements fell outside
  the 0.30 window and the gate never saw them. `FindSimilarActiveFacts` now unions embedding
  hits with pg_trgm text matches (floor 0.40; prod restatements score 0.38-0.84, distinct
  same-topic claims 0.24-0.40); the union reaches all twelve known pairs. (3) A failed gate
  call was a permanent miss (dedupe is decided once, at insert time, and cron logs are not
  retrievable to prove it); the gate now retries once. Lesson: when a judge answers correctly
  on replay but the rows coexist in prod, look at candidate recall and at the error path
  before touching the prompt.

## Extractor: non-position facts rejected, one claim per fact (2026-09-17)

- Facts like "a souhaité un joyeux anniversaire à Jordan", "est décrite comme portant une
  vision plus sociale", "a organisé son meeting de rentrée", "a nommé Y porte-parole", "a
  déclaré avoir grandi dans un quartier populaire", "chacun prend ses responsabilités" were
  extracted and each spawned a junk stance. The reject rules already covered every one of
  these shapes as prose; what was missing was worked ❌ examples, which is what this model
  follows. Added one line of examples per shape (wishes/thanks/tastes, campaign logistics,
  biography, empty formulas, narrated exchanges, passive-voice portraits).
- One claim per fact: a sentence carrying two positions came out as one fact glued with
  "et" (3/3 runs), which no downstream step can dedupe, assign or contradict cleanly. A rule
  in the valid-fact section did nothing (0/3), a self-consistency bullet got 1/3, a worked
  JSON example of one sentence → two facts got 3/3. `TestRemoteLLMExtractor_NonPositionRejection`
  pins both (majority of 3, like the scope test); all five extractor goldens green twice.

## Claim groups replace synthesized meta-facts (2026-09-22)

- A meta-fact was an LLM-written sentence standing in for its members: a statement nobody
  made, with a `meta://` source and no quote, which could lose the checkable details
  (Bayrou's "43,8 milliards, deux jours fériés" became "un plan pour ramener le déficit sous
  un certain niveau"). 107 of 1,381 metas turned out not to hold together at all.
- A claim group is the same 'duplicate' fact_relations, with a real parent: the group's
  most concrete wording (most digits, then longest — one SQL ordering shared with the
  judges' member order). `storage.MergeFactIntoGroup(from, into)` is the one operation for
  every case — a fresh restatement joining, a more concrete one being promoted to parent,
  two groups merging, a synthetic meta being replaced — and moves members, contradictions
  (redundant and self-pairs dropped), group bases, stance slot and stance key-fact ids in
  one transaction. No synthesis call on the fold path any more.
- Groups vs stances, for the record: a stance is the same SUBJECT (few, global), a group is
  the same CLAIM (count once, judge once). 99 of the 107 bad groupings had their members in
  one stance already — the grouping was wrong, the stance was right.
- `dm facts review-groups` un-groups members that no longer belong; nothing is deleted.
  `dm facts migrate-metas` (one-off) flattens nested metas, promotes each meta's most
  concrete real member, then deletes the synthetic rows (guarded: never one with members or
  contradictions). The stance Facts tab lists active facts only, each with "Déclaré N fois"
  and its other wordings and sources.
- Ran on prod 2026-09-23 (1,985 metas: 55 flattened, 1,930 parents promoted, 0 failed; real
  facts, contradictions and stances unchanged). The command and `facts.is_meta` are gone.

## Every contradiction gate sees a group's restatements (2026-09-22)

- A meta-fact went to the judge as its synthesized sentence only; the members' wordings —
  where the figures and specifics live — never reached any prompt. Measured on the Bayrou
  meta: headline alone → NO ("sujets distincts"); with its restatements → YES 3/3 (two
  public holidays he proposed to cut vs "je ne supprimerai aucun jour férié").
- `promptrules.Statement{Text, Also}` is what every judge and veto gate now takes: the batch
  detector, its verifier, the auditor (both passes) and its verifier, `reverify`. Members are
  loaded once per batch (`GroupMemberTexts`, most concrete first, 800-char budget covering
  97 % of groups). A shared `RestatementsRule` tells each judge the wordings are one
  statement, never to be compared with each other (golden: 200 € vs 150→500 € stays NO).
- Rule for adding context to a judge: a gate that can DROP a contradiction must never see
  less than the gate that found it — otherwise the auditor evicts exactly what the new
  context revealed. The stance path is the one exception: its finder (stance analysis) sees
  plain texts, so its verifier does too.
- The judge and audit prompts changed, so the verdict caches invalidate by design: stale
  negatives re-judge at 1,500 a night (10,170 cached, ~7 nights), and every contradiction
  is re-audited once.

## Secrets never reach a log: SerpAPI errors are redacted (2026-09-22)

- SerpAPI takes its key as a query parameter and net/http quotes the whole URL in transport
  errors, so every network failure printed the live key — into the logs and into CI output
  (seen in a test run today). Errors from the SerpAPI requests now pass through
  `redactSecrets`, which rewrites `api_key`/`key`/`token`/`secret`/`password` parameter
  values and drops the error chain on purpose: leaving the `*url.Error` reachable would hand
  an unwrapping caller the raw URL again.

## Every judgement call is counted: decision_counters + Prometheus (2026-09-22)

- "How often does that happen?" had no answer for any gate or judge in the pipeline. The
  question came up over the extractor's scope rule (out-of-scope subjects: singers, coaches)
  and there was nothing to look at — the gate that rejects them returns an error, logs it,
  and the cron logs are not retrievable.
- `metrics.RecordDecision(stage, outcome)` at each decision point, tallied in memory and
  flushed to `decision_counters` (day-bucketed). The database is the transport because the
  judgements happen in the cron process, which Prometheus cannot scrape: the web service
  exports the stored totals as `dm_decisions_total` / `dm_decisions_today`
  {stage, outcome}. Flushed every minute during a run and on `storer.Close()`, so a CLI
  job's counts land too and an outage costs at most a minute.
- Instrumented: entity gate (kept / rejected), fact dedupe (no candidates / duplicate /
  folded / distinct / gate error), stance assign (joined / new / **label_rescue** — joined a
  stance only the label pass surfaced), stance analyze (duplicate / new info / contradiction),
  stance merge (confirmed / rejected, split by whether the pair came from embeddings or from
  the label source), contradictions (found / none / refuted by the verifier / error),
  document extraction (extracted / no facts / failed / skipped). The split labels are there
  to answer "does this mechanism earn its keep?" — the label pass and the label-similarity
  merge source both cost LLM calls and both now report what they contribute.

## One document state machine for every plugin: fetched → extracted | failed (2026-09-22)

- Run 104 (2026-09-22) was lost to a 77-minute tunnel outage: the LLM was unreachable, the
  OpenRouter fallback had no credits, and the night's 26 scraped articles were thrown away
  with it, because a scrape lived only in memory and "already processed" was inferred from
  stored facts. That inference also meant every article whose extraction failed or yielded no
  facts was re-scraped and re-extracted each night while the search window still returned
  it; twitter logged a fetched range only after all its tweets were extracted, so a cut run
  re-bought the range. Debats alone did it right, with its own table.
- `source_documents` + `core.ProcessDocuments`: a plugin's fetch step saves each document
  the moment the fetch succeeds (content included); the engine drives every unfinished
  document of a source type to `extracted` (facts count recorded, content deleted, the row
  kept as the never-fetch-again marker) or `failed` (retried until `MaxExtractionAttempts`).
  A run does not know or care which run fetched a document. The engine owns the scheduling
  debats had: order hook, deadline, max documents, abort on a failure streak (an outage,
  left for the next run), "every attempt failed" → error, so a dead LLM turns the step red
  instead of silently marking things. Plugins supply only the fetch step and
  `extract(batch) → facts per document`; news uses batches of 1, twitter its tweet batches.
  `IsURLVisitable` counts a fetched document as a visit. Saves are batched (one round trip
  per 500 documents): the débats backlog is ~12k documents, and one INSERT each took 25
  minutes against the public proxy before dying on a network blip, having extracted nothing. Debats moved onto the same table
  (`debate_documents` rows migrated as extracted, keyed by permalink) so there is one machine,
  not two; its durable `facts_dropped` count is now only in the run log. Lesson: a step's
  state is written by that step when it finishes, never inferred from a later step's output.

## Fold gate pinned on prod pairs; singleton re-assignment sweep; fold backfill reaches text pairs (2026-09-20)

- Three nights of news folding (272-409 members/night, was 15-62) read as clean on a first
  sample, but a full read of two persons' metas found ~1 in 6 over-folded: same subject
  area, different claim ("wants the PS nomination" + "the useful vote is for him"; "the war
  must end" + "the EU cannot weigh on it"; a 200 € vs a 150→500 € chèque énergie). The
  position gate reproduced all of them: its "lean toward true when the matter is the same"
  let "matter" mean "theme". The prompt now defines the matter as the specific question the
  statement answers, with a reader test and worked ❌ pairs (candidacy vs prediction, demand
  vs assessment, two measures in one field, wish vs judgment, different amounts). The
  `TestFactDedupeAdjudicatorGolden` cases are the prod texts (9 fold-gate pins, verbatim,
  prompt examples are analogues so the golden stays held out): 13/13 x3. One borderline pair
  ("affrontement sur les propos racistes" vs "éviter la bascule vers l'extrême droite") was
  dropped rather than tuned against — both are "fight the far right". The strict gate splits
  a rounding variance ("TVA à 5 %" vs "à 5,5 %"); a clause saying rounding is the same figure
  did not win that case and flipped an unrelated pin in 2/2 runs, so it was reverted. Accepted
  cost: two such facts sit side by side in one stance, which is harmless; the reverse (a hidden
  distinct claim) is not.
- `dm stances reassign-singletons`: 72% of stances were one-fact stances, most created
  before the 2026-09-17 shortlist/decision fixes. The sweep re-runs topic assignment for
  each singleton against the person's other stances (the singleton itself excluded from the
  shortlist) and, on a join, reuses the merge apply path (fact moves, bundles repointed,
  re-analyzed in the new stance, label+summary refreshed). Marine Le Pen: 33 of 48 joined.
  Cost on the serial LLM is ~11 s per decision and ~25 s per join, so 3,900 singletons is
  a multi-day sweep; it is deadline-bound (`--until`) and resumable (`--start-after`), run
  outside the nightly window so it never races insert-time routing on the same person.
- `dm facts fold` gets the inserter's trigram candidate source (`FoldOptions.TextFloor`), so
  the backfill reaches the near-verbatim restatements whose embeddings sit outside the
  cosine window. Prod count: 11,115 candidate pairs, 3,101 of them text-only.
- `dm stances check-placement --sample N` measures the assigner's disagreement with facts
  already sitting in multi-fact stances before any blanket re-assignment is run: each sampled
  fact is re-assigned among ALL of its person's stances, its own included, with the fact itself
  hidden from the evidence (`excludeFactID` on the shortlist queries) so the stance has to earn
  it on its other members. Excluding the current stance would force a move; showing the fact
  as its own nearest neighbour would force a stay.
- Placement check on 200 queue-person facts: 175 stay, 21 move, 3 split. Read against the
  labels, ~3 % are real misfiles, ~2 % are facts shuttling between two stances that are the
  SAME topic under two labels (Bardella: "politique environnementale et régulations
  écologiques" and "Politique environnementale et Pacte vert", centroids at cosine 0.44, far
  under the 0.82 merge recall floor because opposite-direction facts pull centroids apart),
  ~1 % would be regressions, the rest coin flips. A blanket re-assignment of the 11k facts was
  not worth its ~2 LLM-days; the duplicate-stance signal was. The merge sweep gets a second
  candidate source: label trigram similarity ≥ 0.40 (219 pairs over the whole table). Label
  pairs are mostly shared generic words ("France-Hongrie" / "France-Espagne" scores 0.64, and
  the merge decision is known to be loose on related pairs), so they pass a stricter
  same-subject gate (`SameSubjectByLabel`, `TestSameSubjectByLabelGolden` on prod pairs,
  10/10 x3) before the merge decision. The rulebook key includes it, so the nightly re-judges
  every person once.
- `dm facts review-metas --since D`: re-judges the memberships of the metas folded under the
  old gate (clique in id order, as the fold pass builds them). Members that no longer belong
  are unfolded and the meta text rewritten (`--apply`); a meta with fewer than two members
  left is a phantom and is only deleted with `--delete-dissolved`. Unfolded members have no
  stance link and are placed by the nightly orphan routing; their contradictions stay on
  the meta and the fact is re-detected as active. Membership is a MAJORITY vote against the
  kept members, not a clique: the first dry run evicted Le Pen's "TVA à 5,5 % sur le
  carburant" from its own TVA meta because the gate said no once, against one compound
  sibling ("TVA … et suspendre les hausses d'impôts (électricité, gaz, fioul)"), while it
  agreed with the seed and two other members. An over-fold fails against every member; a
  borderline pair fails against one. Complete linkage cannot tell them apart under an
  imperfect gate. And a "different" only counts when the gate repeats it with the two
  statements swapped (the auditor's order-swap agreement): a 2-member meta gets one
  comparison, and the second dry run dissolved ~25 % of the July metas, two in five of a
  sample on a coin-flip verdict ("demande d'inscrire la PPL" / "apporte son soutien à la
  PPL"). Costs one extra call per "different", in this one-off review only.

## Votes are single-subject facts; one dedupe mode (2026-09-23)

- frpolitics stores one fact per ballot, named after its voter or party ("Jean Dupont a voté
  CONTRE …", "Rassemblement National a collectivement voté POUR …"). The embedding text stays
  the name-free object phrase, so every ballot on one scrutin shares one embedding.
- Gone: `StoreFactInput.CoActorIDs`, the shared-fact guards (`SimilarFact.MentionPersons`,
  the multi-mention exclusion of `dm facts fold`), and the inserter's drop mode (tier-1
  auto-drop, `IsSameClaim` gate, provenance attach). `EnableDedupe(groups)` is the one
  switch: every inserter (news, twitter, frpolitics votes and débats, `dm ingest files`)
  groups same-position facts under the LLM gate; without a configured judge nothing is
  deduped. The inserter no longer reads `DEDUPE_AUTO_THRESHOLD`; only `dm dedupe run` does.
- A vote law whose store fails part-way resumes after the ballots already stored instead
  of deleting and redoing them (a grouped ballot may already hold another fact's slot).
- Why: one fact belonging to many persons forced per-person overlays everywhere — 13
  unscoped queries found in the fan-out audit, grouping refused for shared facts, and
  frpolitics kept a drop mode that could lose every co-voter's ballot (mirror-eqm).
- Cost moved to insert time: each ballot is stance-routed and dedupe-gated when inserted
  instead of waiting for the nightly fan-out. Person-scoped stance/contradiction queries stay:
  scoping by person is correct for any fact.

## An unreachable LLM does not use up a document's retries (2026-09-24)

- `llm.ErrUnavailable` marks a call no endpoint could serve (primary down or skipped by the
  breaker, fallback failed too). `core.ProcessDocuments` still aborts the run on the outage
  streak but no longer charges those failures to the documents: `MaxExtractionAttempts`
  counts failures of the document only.
- Why: the local LLM box's wifi dropped three nights running and the fallback had no
  credits; every outage night charged each attempted document one of its three attempts, so a
  third bad night would have abandoned 160 documents for good.

## Stopping the LLM mid-run loses nothing (2026-09-24)

- The user stops the local LLM at any time. `llm.ErrUnavailable` (chat and embeddings) means "no
  decision": no judge records a verdict on it (`MajorityVote` stops; the auditor removes only on
  a full quorum), a document stays pending (`core.FailsDocument`), the entity validator does not
  approve, the audit clock is not reset, a stance link happens only after analysis succeeds.
- `RunDaily` stops starting LLM phases after the first ErrUnavailable (ingest loops stop per
  target/handle/law too) and records the run failed. Other per-item failures are still skipped
  and counted, so one bad answer never blocks a document or a merge.
- Found by an audit, fixed: chunked documents whose chunks all failed were marked extracted and
  their content deleted; news/twitter dropped facts after extraction; a failed vote summary read
  as "irrelevant"; vote resume was keyed on LLM-generated text; `MaxLaws` was spent on unusable
  laws; leftover documents waited for new items; dismissed pairs (NULL judge_hash) were re-opened
  by the stale-verdict re-judge (`fact_pair_checks.dismissed`).
- Lesson: any "log and continue" around an LLM call is a place where an outage becomes a final
  decision. Propagate ErrUnavailable; fail open only for documented non-outage errors.

## The daily run uses the GPU through the day (2026-09-24)

- One run deadline, 23:00 Europe/Paris (was an 11:00 backfill deadline): no phase starts after
  it, and the backfill stops starting work then. It ends the run ~4 h before the next 01:00 UTC
  cron, which the host skips while a run is still active.
- Per-phase caps tripled (stale-verdict re-judge 4500, translations 4500, stance fan-out 750,
  vote laws 30). Every phase derives its pending set from the DB, and stopping the LLM mid-run
  now loses nothing, so the user stops llama-swap whenever the GPU is needed.

## The daily run waits out LLM outages (2026-09-25)

- On `llm.ErrUnavailable` a phase no longer stops the run: `runGate.retry` waits for the LLM
  (`llm.WaitForPrimary`: `/models` plus one chat warm-up per 2-min poll, first poll after one
  interval, even without a fallback) and reruns the phase from its DB-derived pending set. An
  LLM still down at the run deadline stops the run as failed; nothing (re)starts after it; a
  smoke run does not wait.
- Loop guard: a phase that keeps failing as unavailable while the LLM answers on the first poll
  (one request Cloudflare cuts or the server 500s on) is given up after 2 retries as a step
  error, and the run goes on (an ingest/backfill plugin is abandoned, the next ones run).
- A retry does not redo paid work: `IngestRun`/`BackfillRun` skip finished plugins; plugins
  report `IngestStats.Done` (targets finished), `Fetched` (fetched, extraction interrupted →
  `IngestOptions.ExtractOnly`: news extracts the stored documents, no new search) and `Spent`
  (frpolitics laws against the 30-law cap); fan-out and translation budgets carry over
  (`budgeted`); the stale-verdict eviction runs once; the daily vote feed ignores the 24h
  revisit window so a law stopped part-way resumes in the run.
- A recovered run is not failed: outages and time waited are noted in the log, Telegram and
  `pipeline_runs.error_summary` (`step_errors` and status unchanged). Why: a 2.5-minute wifi
  drop stopped a run at 05:39 UTC and wasted the day's GPU window.

## Subject names are grounded in the source (2026-09-25, mirror-dm5)

- The extractor misspells subject names ("Édoued Philippe", "jean-1uc melenchon",
  "manuel b_ompard") and persons match by exact folded name, so every misspelling became
  a person of its own: ~180 such rows in prod, their facts cut off from the real person
  and out of reach of contradiction detection. The entity validator did not stop them:
  its ACCEPT D (close spelling variant) accepts exactly this.
- `categorizer.groundSubject` runs on every extracted fact: a subject whose normalized
  name is neither the target nor in the source chunk takes the closest spelling found
  there (≤2 edits, no word changed by a third of its letters; ≤3 for malformed names
  with digits, underscores or escapes), and the fact text is rewritten with it.
  Counted as `fact_parse/subject_repaired`.
- The budget is tight on purpose: real near-namesakes (Bayrou/Baroin, Le Pen/Le Guen,
  Marine/Marion) sit 2-3 edits apart, and a false repair moves facts onto another real
  person, which is worse than a split person. Lesson: fixing a fuzzy-match problem with
  a fuzzier match needs negative cases from the same domain before the thresholds.

## Extraction writes ambiguous words in their unambiguous form (2026-09-25, mirror-ezt)

- New self-consistency rule UNAMBIGUOUS WORDS: « dès le primaire » → « dès l'école
  primaire », « une primaire » → « une primaire électorale », in the fact text and the
  embedding text. The homonym clause in the false-positive catalog only helps once a
  pair reaches a judge; writing the sense into the fact keeps homonyms apart in KNN
  pairing, stance routing and every judge prompt at once. Golden: old prompt 0/3, new 3/3.

## The daily débats feed stays in its window (2026-09-27, mirror-29j)

- `ProcessDocuments` lists every unfinished document of a source type, so since the document
  state machine (e3ae59a) the daily débats feed (`Since` = 4 days, no deadline) walked the
  whole transcript backlog: run 108 was still in its ingest phase 40 h later, and the host
  skips the next cron while a run is active. `ProcessOptions.PublishedSince` now floors the
  listing by publication date; `RunDebats` passes its `Since`. The backlog is only the
  deadline-bound backfill's job.
- Lesson: a phase without a deadline must be bounded by its input. When a listing moves to a
  shared "everything unfinished" query, re-check every caller's implicit bound.

## One deadline context for the whole run; ingest has its own budget (2026-10-01, mirror-dkc)

- Run 111 stayed 28 h in the ingest phase (one stance routing per ballot, ~65 min of GPU
  per law, 30 laws per run): the deadline was only checked between phases, so the run
  never reached detection, the audit or the translations, and the host skipped the next cron.
- `RunDaily` now derives one `work` context cancelled at the run deadline (23:00 Paris)
  and gives the ingest phase a child context with `ingestBudget` (12 h). Plugins and phases
  get no deadline logic of their own: every loop stops at the next item when its context is
  done, charges nothing to the cut item, and returns the context error wrapped with %w.
  The pipeline records such a phase as "stopped at the deadline" (a note, not a step error);
  the next run resumes it from its pending set. Bookkeeping after the phases keeps the
  original context. `dailyMaxLaws` 30 → 10. The stance merge sweep got the same treatment
  (`mergeBudget`, 2 h, 2026-10-02) after it used the afternoon of run 112; swept persons are
  memoized, so a cut sweep resumes with the next person.
- Lesson: a pgx write with an expired context fails, which hid most "done marker after
  cancel" bugs but also broke the one write that must happen: the rollback of a fact whose
  stance routing was cut now runs under `context.WithoutCancel`.

## History step: news and twitter read back three years, a month per run (2026-10-01)

- A plugin that can read its source by date range implements `plugins.HistoryReader`
  (news, twitter). Phase 8 walks each one back from its cursor (`history_cursors`), in
  30-day stretches of 3-day windows for the queue targets, through the ordinary
  `IngestRun`, until the deadline closes the gate or three years are reached.
- The plugin's own fetch log decides when a stretch is done (`HistoryFetched`): news logs
  every window searched and scraped per person (`news_search_log.date_from/date_to`, never
  searched again), twitter its `twitter_fetch_log`. So a cut read resumes at the next
  unfetched window the next night, and a stretch with a target the plugin could not fetch
  (an error, a search quota) is read again, never skipped. Stretches are whole UTC days.
- Twitter's `last_fetched_at` (where the daily fetch starts) only moves forward: reading an
  older range used to set it to now and the daily fetch skipped the tweets in between.
- The GPU is the limit: a month of the 24 queue persons costs ~33 h at the 2026-09-28 pace.

## Reports of one event form one claim group; `dm facts group` (2026-10-01)

- The same-position gate (fold) had a rule for "different concrete events" and for
  "different numbers", none for several reports of ONE event: five articles on Marine Le
  Pen's 2026-07-07 appeal ruling ("15 mois d'inéligibilité" / "45 mois dont 30 avec
  sursis") stayed five facts, and her TF1 innocence claim got six contradiction rows.
- New rule, by kind of fact rather than by domain: an event or outcome happens once, so its
  reports are one claim whatever details or number framing they use; for a statement or
  proposal the numbers are the claim itself (Glucksmann 200 € vs 500 € stay apart), and
  different occasions (first instance vs appeal, two votes) stay apart. Golden 18/18, 3 runs.
- First prod run of it (2026-10-01, stopped after 18 groups, reviewed by hand): 9 right
  (one election result, one motion, one ruling reported several times), 9 wrong: votes on
  different amendments of one bill grouped as "one event", and a compound fact grouped with
  one of its claims. Rules added: a vote is one ballot on one text; a bundle of claims is
  never the same as one of them; an appeal ruling and the first-instance ruling are two
  events. The wrong ones were un-grouped (`dm facts ungroup`). Golden 26 cases, 156/156
  over two batches. Lesson: sample a few persons dry-run and review before any full run.
- `dm facts group --facts a,b,… [--apply]` puts chosen facts into one claim group under the
  most concrete wording, whatever the gate decided (the operator override), then recomputes
  scores; `dm facts ungroup` takes facts out again. Existing facts only regroup through
  `dm facts fold` or these commands.

## Same-position gate: "same" must hold in both orders (2026-10-02)

- A Bardella dry run (no writes) proposed 53 groups, about half wrong: different claims in
  one area ("stopping immigration first" / a Channel border promise) and bundles grouped
  with one of their claims. The gate already did this before the same-event rule.
- The gate reads the second statement against the first, so a pair where one says more
  than the other often reads as a restatement one way only. `IsSamePosition` now asks again
  with the statements swapped and groups only when both say same (one more call, only for
  pairs the first call groups). Golden: 37 cases, 93/111 → 105/111; the two remaining
  misses are logged as known gaps (an explicit example for one of them broke three others).
- The fact extractor asks again once (T=0.3) when a reply is not valid JSON: one malformed
  reply used to fail a handle's whole tweet batch and charge every tweet an attempt.

## The nightly stance fan-out phase is removed (2026-10-03)

- The fan-out routed (person, fact) pairs that were in none of the person's stances. It was
  added when one vote fact belonged to many persons. Since one fact per voter, insertion routes
  every fact to its stance or rolls the fact back, so the phase found 2 facts in run 113.
- An orphan is now an invariant violation: the `facts_without_stance` and
  `stance_routings_pending_total` gauges report it, and `dm facts assign-stances` fixes it.
  The pipeline has 8 phases.

## Nested grouping was tried and dropped (2026-10-03)

- Nested rule: a fuller wording that contains another one joins it. Two versions were tried:
  a difference list ("what only A says, what only B says") and a one-direction question
  with the fuller statement first.
- The difference list failed: the local model lists trivial differences ("menaces de mort"
  vs "menaces de mort explicites"), so no pair groups (golden 69/111).
- The one-direction question scored 102/111. On a dry run of 5 persons, judged with one
  definition, it merged different claims in 36 % of its groups; the deployed strict check
  (both orders must say same) did so in 24 %. The strict check stays.
- Lesson: compare two rules on the same data with the same judge definition. Two judge
  runs with two definitions gave numbers that looked comparable and were not.

## LLM replies give the reasoning first and the verdict last (2026-10-03)

- Rule (CLAUDE.md, Conventions): every decision prompt asks for the reasoning before the
  decision field, and the decision is the last field. A verdict written first is a guess,
  and the reasoning after it only justifies the guess.
- Changed: fold gate, entity dedupe, stance merge, complaint triage, entity filter,
  verifier, bundler (it gets a reasoning line and 256 tokens), stance analysis JSON.
- Measured on the goldens:
  - fold gate: 105/111 before and after; false merges 2 to 0, missed merges 0 to 2.
  - analyze: 6/6. Label gate: 10/10.
  - verifier: 44/44 to 42/44 (TP_114, FP_84 now fail).
- The verifier had no YES pattern for the self-exempting double standard. The old order
  passed that case by instinct; with reasoning first the model followed the procedure and
  dropped it. The pattern is now in the verifier's YES list.
- A reason line that names the matching pattern first is worse (38/44): the model picks a
  NO pattern from the long catalog and stops there.

## A group is one occurrence; the gate reads the evidence (2026-10-05)

- A claim group means "one occurrence, reported N times": the same claim, act or event on
  the same occasion. The same position stated again on another occasion is a new
  occurrence; the stance holds those together. Votes are never grouped.
- Why: the old gate saw only the two fact texts. No reader can tell a second report from
  another statement on the same topic without the quotes, the dates and the outlets. The
  old "same position" definition also mixed two relations, one factual (same occurrence)
  and one with no clear limit (same position over time).
- Rule (`claim_evidence.go`), in insertion, `dm facts fold` and `dm facts review-groups`:
  - evidence, in SQL: free-text sources only, and quotes with pg_trgm similarity >= 0.30
    OR publication dates <= 7 days apart;
  - then the gate `IsSameOccurrence`, which reads each fact with quotes, dates, outlets,
    and must say groupable in both orders.
- "Bundle" (one fact holds the other's claim plus more, same occasion) is groupable, with
  the fuller fact as the parent. This replaces "a bundle is not one of its claims": the
  model and the labelers could not separate "same" from "bundle" in a stable way.
- Measured on 207 prod pairs that the old gate grouped (labels made with the evidence):
  old gate 27 % clear errors; quote overlap alone finds 69 % of the true groups; this rule
  finds 93 %, with 5 % clear errors and 12 % borderline.
- Lessons:
  - Give a judge the evidence a human would use before tuning its prompt.
  - The model reads two dates 3 weeks apart and still says "same": a rule the model
    ignores must be code.
  - Labels made by a blind judge are noise; the prompt tuning against them was a seesaw.
- Golden: `TestOccurrenceGateGolden` (36 prod pairs with evidence, `testdata/`). The 33
  text-only fold cases are removed. Existing prod groups are not yet reviewed (mirror-vpj).
