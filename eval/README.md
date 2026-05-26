# End-to-end contradiction-detection eval

A synthetic test corpus + harness for measuring the contradiction detector's
precision and recall. Three fictional politicians × three stances each × four
facts per stance = 36 facts, plus an explicit list of pair-level expected
verdicts (`expected_pairs`).

The corpus is fictional: invented names, dates, factory locations, votes,
court rulings. The goal is to exercise every rule in the contradiction prompt
(HARD GATE, DATE-ONLY, VALUES-BASED, conviction_vs_acknowledgment,
announcement_then_action, learning_over_time, accusation_not_finding, ...)
with at least one positive and one negative example each.

## Files

- `cases.yaml` — the corpus. Edit this to add cases. **Every pair NOT listed
  in `expected_pairs` is implicitly expected to be NOT contradictory**, so any
  positive flag on an unlisted pair is counted as a false positive.
- `../backend/eval/` — loader + evaluator package.
- `../cmd/eval.go`, `../cmd/eval_run.go`, `../cmd/eval_validate.go` — CLI.

## Workflow

Prerequisites: a local Supabase (or any Postgres reachable via `DATABASE_URL`)
and a local LLM gateway exposing OpenAI-compatible `/chat/completions` and
`/embeddings` endpoints. Set `OPEN_AI_API`, `OPEN_AI_KEY`, `OPEN_AI_CHAT_MODEL`,
and `OPEN_AI_EMBED_MODEL` to point at it.

```bash
# 1. Lint the YAML without touching the DB.
dm eval validate

# 2. Run the full eval. Resets corpus persons' facts + contradictions, ingests
#    every fact through the normal FactInserter + stance manager pipeline,
#    runs contradiction detection with --check-all (so the LLM prompt is
#    exercised on every pair regardless of KNN distance), then compares
#    detected contradictions against expected_pairs.
dm eval run

# 3. Re-run without wiping (useful to iterate on detection-only changes).
dm eval run --keep-data
```

Sample report shape:

```
=== EVAL REPORT ===
Pairs evaluated:      20
True  positives (TP): 11
False negatives (FN): 2   (missed real contradictions)
False positives (FP): 3   (unwarranted flags; 1 unexpected pairs)
True  negatives (TN): 5

Precision: 0.786
Recall:    0.846
F1:        0.815

--- per rule (Expected | TP | FN | FP | TN) ---
accusation_not_finding             1 | 0 | 0 | 0 | 1
announcement_then_action           1 | 0 | 0 | 0 | 1
conviction_vs_acknowledgment       1 | 0 | 0 | 0 | 1
conviction_vs_denial               1 | 1 | 0 | 0 | 0
denial_after_claim                 3 | 2 | 1 | 0 | 0
learning_over_time                 1 | 0 | 0 | 0 | 1
third_party_narrative              2 | 0 | 0 | 0 | 2
values_based                       8 | 6 | 1 | 1 | 0
... per-pair outcomes follow ...
```

## When you change the prompt

1. Run `dm eval run` and capture the precision / recall / F1.
2. Make the prompt change.
3. Re-run `dm eval run`. Compare. If F1 goes down, ask whether the new rule is
   worth the regression. If F1 goes up, ship.

## When you add a new rule to the contradiction prompt

Add at least one positive and one negative example pair to `cases.yaml` that
exercises the new rule. This keeps the eval honest as the rule set grows.

## Why a separate eval, not a unit test

The contradiction prompt depends on stance-document context (the LLM sees a
stance summary alongside the pair), the embedding model's similarity
judgments, and the dedup + stance-routing pipeline. Pure unit tests can mock
each of those but won't catch interactions. The eval runs the full pipeline
on a local DB so the integration story is what's measured.
