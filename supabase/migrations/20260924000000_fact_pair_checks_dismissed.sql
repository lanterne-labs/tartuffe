-- +goose Up

-- dismissed marks a negative pair verdict recorded by dismissing a stored
-- contradiction (auditor eviction, `dm contradictions dismiss`, the
-- cross-person prune) rather than by the detector. The nightly re-judge of
-- stale negatives (judge_hash not current) skips these rows: their NO comes
-- from the auditor or a person, not from the detector's rulebook, and
-- re-opening them would let the detector re-raise what was dismissed.
ALTER TABLE fact_pair_checks ADD COLUMN IF NOT EXISTS dismissed BOOLEAN NOT NULL DEFAULT false;

-- Backfill the dismissals recorded before the column existed. Since the
-- judge_hash migration (20260915100000, applied 2026-09-15 11:18:55) the
-- detector stamps every verdict with a hash; only a dismissal writes a
-- negative with a NULL judge_hash.
UPDATE fact_pair_checks SET dismissed = true
WHERE is_contradiction = false AND judge_hash IS NULL AND checked_at >= '2026-09-15 11:18:55+00' AND NOT dismissed;

-- +goose Down
ALTER TABLE fact_pair_checks DROP COLUMN IF EXISTS dismissed;
