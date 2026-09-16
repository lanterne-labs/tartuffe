package contradiction

import (
	"context"
	"digital-mirror/backend/storage"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// ReverifyOptions configures RunReverify.
type ReverifyOptions struct {
	// PersonID restricts the pass to one person when set.
	PersonID *int64
	// Max caps the number of contradictions reverified (0 = no cap).
	Max int
	// Random samples contradictions in random order instead of newest-first.
	Random bool
	// Scope is a human-readable label for the log line.
	Scope string
	// Apply — when true, a dropped row is re-verified a SECOND time and DELETED
	// only when both passes agree it is a false positive (2-of-2 drop); an
	// unstable row is kept. Deleting a contradiction also clears its fact-pair
	// check, so the daily detector re-examines the pair on a later run.
	Apply bool
	// OnRow receives each row's verdict as it is produced. A non-nil error
	// aborts the run. Nil = silent.
	OnRow func(ReverifyRow) error
}

// ReverifyRow is one reverified contradiction as handed to
// ReverifyOptions.OnRow. Its JSON tags are the JSONL schema
// `dm contradictions reverify` writes to stdout.
type ReverifyRow struct {
	ContradictionID int64  `json:"id"`
	PersonID        int64  `json:"person_id"`
	Person          string `json:"person"`
	FactAID         int64  `json:"fact_a_id"`
	FactBID         int64  `json:"fact_b_id"`
	FactA           string `json:"fact_a"`
	FactB           string `json:"fact_b"`
	OriginalReason  string `json:"original_reason"`
	Verdict         string `json:"verdict"` // "keep" | "drop" | "error"
	Refutation      string `json:"refutation,omitempty"`
	Deleted         bool   `json:"deleted,omitempty"` // true when Apply removed this row (2-of-2 drop)
	Error           string `json:"error,omitempty"`
}

// ReverifyResult is the outcome of a reverify pass.
type ReverifyResult struct {
	Total    int
	Keep     int
	Drop     int
	Deleted  int
	Unstable int
	Errors   int
	Elapsed  time.Duration
}

// RunReverify runs the adversarial verification (refute) pass — the VERIFIER
// judge that gates the live detection path, distinct from the AUDITOR behind
// RunAudit — over recorded contradictions and reports the ones it would now
// drop. Read-only unless Apply.
func RunReverify(ctx context.Context, storer *storage.PostgresStorer, opts ReverifyOptions) (*ReverifyResult, error) {
	rows, err := storer.ListContradictions(ctx, opts.PersonID, opts.Random, opts.Max)
	if err != nil {
		return nil, fmt.Errorf("failed to list contradictions: %w", err)
	}
	result := &ReverifyResult{Total: len(rows)}
	if len(rows) == 0 {
		return result, nil
	}

	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	startedAt := time.Now()
	log.Info().Int("total", len(rows)).Bool("random", opts.Random).Bool("apply", opts.Apply).Str("scope", opts.Scope).Msg("Starting contradiction reverify")

	for i, row := range rows {
		line := ReverifyRow{
			ContradictionID: row.ID,
			PersonID:        row.PersonID,
			Person:          row.PersonName,
			FactAID:         row.FactAID,
			FactBID:         row.FactBID,
			FactA:           row.FactAText,
			FactB:           row.FactBText,
			OriginalReason:  row.Reason,
		}

		survives, refutation, vErr := detector.VerifyContradiction(ctx, row.FactAText, row.FactBText, row.Reason)
		switch {
		case vErr != nil:
			result.Errors++
			line.Verdict = "error"
			line.Error = vErr.Error()
			log.Error().Err(vErr).Int64("contradiction_id", row.ID).Str("person", row.PersonName).Msg("Reverify error")
		case survives:
			result.Keep++
			line.Verdict = "keep"
			line.Refutation = refutation
		default:
			result.Drop++
			line.Verdict = "drop"
			line.Refutation = refutation
			log.Warn().
				Int64("contradiction_id", row.ID).
				Str("person", row.PersonName).
				Str("original_reason", row.Reason).
				Str("refutation", refutation).
				Msg("Verifier would DROP this contradiction (false positive)")

			// Apply: confirm with a second independent pass before deleting,
			// so the local model's run-to-run variance can't delete a row on a
			// fluke. Only a 2-of-2 drop is removed; a disagreeing pass keeps it.
			if opts.Apply {
				confirmKeep, confirmReason, cErr := detector.VerifyContradiction(ctx, row.FactAText, row.FactBText, row.Reason)
				switch {
				case cErr != nil:
					log.Warn().Err(cErr).Int64("contradiction_id", row.ID).Msg("Confirmation pass errored; keeping row (not deleted)")
				case confirmKeep:
					// Second pass disagrees (it would KEEP) — unstable, do not delete.
					result.Unstable++
					log.Info().Int64("contradiction_id", row.ID).Str("confirm_reason", confirmReason).Msg("Confirmation pass disagreed (keep); row is unstable, NOT deleting")
				default:
					if err := storer.DeleteContradiction(ctx, row.ID); err != nil {
						log.Error().Err(err).Int64("contradiction_id", row.ID).Msg("Failed to delete contradiction")
					} else {
						result.Deleted++
						line.Deleted = true
						log.Warn().Int64("contradiction_id", row.ID).Str("person", row.PersonName).Msg("Deleted contradiction (2-of-2 drop)")
					}
				}
			}
		}

		if opts.OnRow != nil {
			if err := opts.OnRow(line); err != nil {
				return result, err
			}
		}

		if (i+1)%10 == 0 {
			log.Info().Int("done", i+1).Int("total", len(rows)).Int("keep", result.Keep).Int("drop", result.Drop).Int("deleted", result.Deleted).Int("errors", result.Errors).Msg("Reverify progress")
		}
	}

	result.Elapsed = time.Since(startedAt)
	log.Info().Int("total", len(rows)).Int("keep", result.Keep).Int("drop", result.Drop).Int("deleted", result.Deleted).Int("unstable", result.Unstable).Int("errors", result.Errors).Bool("apply", opts.Apply).Dur("elapsed", result.Elapsed).Msg("Contradiction reverify complete")
	return result, nil
}
