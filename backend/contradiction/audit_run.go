package contradiction

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// AuditOptions configures RunAudit. The same code path serves the
// `dm contradictions check` CLI and the daily-pipeline auditor step — the
// differences are: whether per-row output is wanted, and whether the run gets
// recorded to job_metrics (which gates the pipeline cadence).
type AuditOptions struct {
	// PersonID restricts the audit to one person when set.
	PersonID *int64
	// Max caps the number of contradictions audited (0 = no cap).
	Max int
	// Random samples contradictions in random order instead of newest-first.
	Random bool
	// Scope is a human-readable label for the log line ("all persons", "Marine Le Pen", etc.).
	Scope string
	// RecordMetric — when true, a job_metrics row is inserted after an audit
	// that judged its rows (job_type="contradiction-audit"). The daily
	// pipeline gate reads this timestamp to decide whether enough time has
	// passed since the last run; CLI invocations leave it false. An audit
	// stopped by an unavailable LLM or a cancelled ctx, or whose every
	// verdict failed, records nothing, so the next run audits again.
	RecordMetric bool
	// Apply — when true, a verdict=remove triggers a confirming SECOND auditor
	// pass and, on a 2-of-2 remove, the contradiction is DELETED (via
	// DismissContradictionAsFalsePositive: records a negative pair check so the
	// detector won't re-raise it, and recomputes the owner's honesty score). A
	// disagreeing second pass keeps the row (counted as unstable). Default false
	// (read-only advisory).
	Apply bool
	// OnRow receives each audited row's verdict as it is produced (rows whose
	// pinned KEEP hash still matches are not re-judged and not reported). A
	// non-nil error aborts the run. Nil = silent (pipeline mode).
	OnRow func(AuditRow) error
}

// AuditRow is one audited contradiction as handed to AuditOptions.OnRow. Its
// JSON tags are the JSONL schema `dm contradictions check` writes to stdout.
type AuditRow struct {
	ContradictionID int64    `json:"id"`
	PersonID        int64    `json:"person_id"`
	Person          string   `json:"person"`
	FactAID         int64    `json:"fact_a_id"`
	FactBID         int64    `json:"fact_b_id"`
	FactA           string   `json:"fact_a"`
	FactB           string   `json:"fact_b"`
	DateA           string   `json:"date_a,omitempty"`
	DateB           string   `json:"date_b,omitempty"`
	OriginalReason  string   `json:"original_reason"`
	Verdict         string   `json:"verdict"`
	Rule            string   `json:"rule,omitempty"`
	AuditReasoning  string   `json:"audit_reasoning"`
	Deleted         bool     `json:"deleted,omitempty"` // true when Apply removed this row (2-of-2 remove)
	Error           string   `json:"error,omitempty"`
	SourceURLsA     []string `json:"source_urls_a,omitempty"`
	SourceURLsB     []string `json:"source_urls_b,omitempty"`
}

// AuditRunResult is the return value of RunAudit — callers (especially the
// daily pipeline) use the counts for logging and Telegram summaries.
// RemoveIDs is the actionable bit: each ID is a contradiction the auditor
// flagged as a false positive (verdict=remove).
type AuditRunResult struct {
	Total     int
	Keep      int
	Remove    int
	Ambiguous int
	Errors    int
	RemoveIDs []int64
	// Cached counts rows skipped because their pinned KEEP hash still matches
	// (verdict unchanged by construction — see AuditInputHash).
	Cached int
	// Deleted counts rows removed under Apply (order-swapped unanimous remove
	// on the SAME rule + verifier veto). Unstable counts remove verdicts whose
	// confirming pass or verifier disagreed (kept). Both stay 0 when Apply is
	// false.
	Deleted  int
	Unstable int
	Elapsed  time.Duration
}

// RunAudit walks every recorded contradiction matching opts, asks the LLM
// auditor to re-judge it against the current ContradictionRules, and emits
// per-verdict Prometheus counters. Without Apply it deletes nothing — when
// the auditor flags a row as a false positive (VerdictRemove), the ID lands
// in result.RemoveIDs so a downstream caller can act.
//
// The run stops at the first call the LLM could not serve and returns an
// error wrapping llm.ErrUnavailable, and when ctx is cancelled with an error
// wrapping ctx.Err(); a run where every verdict failed returns an error too.
// None records the job metric (see AuditOptions.RecordMetric).
func RunAudit(ctx context.Context, storer *storage.PostgresStorer, opts AuditOptions) (*AuditRunResult, error) {
	rows, err := storer.ListContradictions(ctx, opts.PersonID, opts.Random, opts.Max)
	if err != nil {
		return nil, fmt.Errorf("failed to list contradictions: %w", err)
	}

	result := &AuditRunResult{Total: len(rows)}
	if len(rows) == 0 {
		log.Info().Msg("Contradiction audit: no contradictions in scope, nothing to do")
		if opts.RecordMetric {
			recordAuditJobMetrics(ctx, storer, result)
			metrics.IncrementContradictionAuditRuns()
		}
		return result, nil
	}

	auditor, err := NewOpenAIContradictionAuditorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create auditor: %w", err)
	}

	// A side that stands for a group of restatements is judged with its other
	// wordings, as the detector judged it: an eviction gate must never see
	// less than the gate that found the contradiction.
	factIDs := make([]int64, 0, 2*len(rows))
	for _, row := range rows {
		factIDs = append(factIDs, row.FactAID, row.FactBID)
	}
	members, err := storer.GroupMemberTexts(ctx, factIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load group members: %w", err)
	}

	// When applying, a deletion also requires the VERIFIER (refute pass) to agree.
	// The verifier is a second, independent, reversal-aware judge: it keeps genuine
	// reversals/broken-promises the auditor sometimes mislabels into a fuzzy bucket,
	// so it vetoes those deletions. Only built when Apply (it costs an extra call).
	var verifier *OpenAIContradictionDetector
	if opts.Apply {
		verifier, err = NewOpenAIContradictionDetectorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to create verifier for --apply cross-check: %w", err)
		}
	}

	startedAt := time.Now()
	// stopped is set when the LLM went away or ctx was cancelled: the audit
	// stops at that row, which is re-judged by the next run.
	var stopped error
	log.Info().
		Int("total", len(rows)).
		Bool("random", opts.Random).
		Str("scope", opts.Scope).
		Msg("Starting contradiction audit")

	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			stopped = err
			break
		}
		stmtA := promptrules.Statement{Text: row.FactAText, Also: members[row.FactAID]}
		stmtB := promptrules.Statement{Text: row.FactBText, Also: members[row.FactBID]}
		// Verdict pinning: a row audited KEEP under identical inputs (facts and
		// their other wordings, dates, reason, prompt/rulebook) is not
		// re-judged — the verdict is a pure function of those inputs, so
		// re-sampling could only add noise.
		inputHash := AuditInputHash(stmtA, stmtB, row.FactADate, row.FactBDate, row.Reason)
		if row.LastAuditHash != nil && *row.LastAuditHash == inputHash {
			result.Keep++
			result.Cached++
			metrics.IncrementContradictionAudits("cached")
			log.Debug().Int64("contradiction_id", row.ID).
				Msg("Audit verdict pinned (inputs unchanged); skipping re-judge")
			continue
		}

		auditResult := auditor.Audit(ctx, stmtA, stmtB, row.FactADate, row.FactBDate, row.Reason)
		if errors.Is(auditResult.Err, llm.ErrUnavailable) {
			stopped = auditResult.Err
			break
		}
		if auditResult.Err != nil && ctx.Err() != nil {
			// Cut by the cancelled run, not a verdict error of the row.
			stopped = ctx.Err()
			break
		}

		line := AuditRow{
			ContradictionID: row.ID,
			PersonID:        row.PersonID,
			Person:          row.PersonName,
			FactAID:         row.FactAID,
			FactBID:         row.FactBID,
			FactA:           row.FactAText,
			FactB:           row.FactBText,
			DateA:           formatAuditDate(row.FactADate),
			DateB:           formatAuditDate(row.FactBDate),
			OriginalReason:  row.Reason,
			Verdict:         string(auditResult.Verdict),
			Rule:            auditResult.Rule,
			AuditReasoning:  auditResult.Reasoning,
			SourceURLsA:     row.FactASourceURLs,
			SourceURLsB:     row.FactBSourceURLs,
		}
		if auditResult.Err != nil {
			result.Errors++
			line.Error = auditResult.Err.Error()
			metrics.IncrementContradictionAudits("error")
			log.Error().Err(auditResult.Err).
				Int64("contradiction_id", row.ID).
				Str("person", row.PersonName).
				Msg("Audit error")
		} else {
			switch auditResult.Verdict {
			case VerdictKeep:
				result.Keep++
				metrics.IncrementContradictionAudits("keep")
				if err := storer.SetContradictionAuditHash(ctx, row.ID, inputHash); err != nil {
					log.Warn().Err(err).Int64("contradiction_id", row.ID).
						Msg("Failed to pin audit KEEP verdict (row will be re-judged next audit)")
				}
			case VerdictRemove:
				log.Warn().
					Int64("contradiction_id", row.ID).
					Str("person", row.PersonName).
					Str("rule", auditResult.Rule).
					Str("audit_reasoning", auditResult.Reasoning).
					Msg("Auditor flagged contradiction as false positive (verdict=remove)")

				// Apply: deletion additionally requires an ORDER-SWAPPED audit
				// pass to independently reach remove on the SAME rule, plus the
				// verifier's agreement. A verdict that flips under statement
				// order or can't reproduce its own justification is treated as
				// confabulation and kept. A row whose check the LLM could not
				// serve is not counted: the audit stops and re-judges it next
				// time.
				if opts.Apply {
					line.Deleted, stopped = applyAuditRemoval(ctx, storer, auditor, verifier, row, stmtA, stmtB, auditResult.Rule, result)
					if stopped != nil {
						break
					}
				}
				result.Remove++
				result.RemoveIDs = append(result.RemoveIDs, row.ID)
				metrics.IncrementContradictionAudits("remove")
			case VerdictAmbiguous:
				result.Ambiguous++
				metrics.IncrementContradictionAudits("ambiguous")
			}
		}

		if stopped != nil {
			break
		}
		if opts.OnRow != nil {
			if err := opts.OnRow(line); err != nil {
				return result, err
			}
		}

		if (i+1)%10 == 0 {
			log.Info().
				Int("audited", i+1).
				Int("total", len(rows)).
				Int("keep", result.Keep).
				Int("remove", result.Remove).
				Int("ambiguous", result.Ambiguous).
				Int("errors", result.Errors).
				Msg("Audit progress")
		}
	}

	result.Elapsed = time.Since(startedAt)

	if stopped != nil {
		return result, fmt.Errorf("contradiction audit stopped, next run audits again: %w", stopped)
	}
	if toJudge := result.Total - result.Cached; toJudge > 0 && result.Errors == toJudge {
		return result, fmt.Errorf("contradiction audit: every verdict failed (%d rows)", toJudge)
	}

	if opts.RecordMetric {
		recordAuditJobMetrics(ctx, storer, result)
		metrics.IncrementContradictionAuditRuns()
	}

	log.Info().
		Int("total", result.Total).
		Int("keep", result.Keep).
		Int("remove", result.Remove).
		Int("ambiguous", result.Ambiguous).
		Int("errors", result.Errors).
		Dur("elapsed", result.Elapsed).
		Ints64("remove_ids", result.RemoveIDs).
		Msg("Contradiction audit pass complete")

	return result, nil
}

// applyAuditRemoval decides whether a row the first auditor pass flagged for
// removal is actually deleted. A deletion requires THREE independent signals to
// agree, so a genuine contradiction the auditor mislabels never gets auto-evicted:
//  1. an ORDER-SWAPPED second auditor pass (statement B first) also returns
//     remove AND cites the SAME rule token. A true false positive is false in
//     both directions for the same reason; a verdict that flips under order, or
//     reproduces "remove" with a different justification, is confabulation
//     (mirror-bii: the Vuibert eviction drew announcement_then_action one pass
//     and different_subjects the next), and
//  2. the VERIFIER (refute pass) — a different, reversal-aware judge — also drops
//     it. The verifier keeps reversals / broken promises / denounce-then-ally
//     pairs, so it vetoes those deletions even when the auditor is confident.
//
// All calls run at temperature 0, so the whole gate is a deterministic function
// of the pair — the same row can no longer survive four audits and die on the
// fifth. On all three agreeing it dismisses the contradiction (delete + negative
// pair check + score recompute). Any disagreement or error keeps the row
// (counted as unstable). Mutates result.Deleted / result.Unstable; returns true
// iff deleted. The error is non-nil only when the LLM was unavailable (it
// wraps llm.ErrUnavailable) or ctx was cancelled (it is ctx.Err()); the row is
// then kept and not counted.
func applyAuditRemoval(
	ctx context.Context,
	storer *storage.PostgresStorer,
	auditor *OpenAIContradictionAuditor,
	verifier *OpenAIContradictionDetector,
	row storage.ContradictionAuditRow,
	stmtA, stmtB promptrules.Statement,
	firstRule string,
	result *AuditRunResult,
) (bool, error) {
	// Signal 2: order-swapped auditor pass — same verdict AND same rule required.
	confirm := auditor.Audit(ctx, stmtB, stmtA, row.FactBDate, row.FactADate, row.Reason)
	switch {
	case errors.Is(confirm.Err, llm.ErrUnavailable):
		return false, confirm.Err
	case confirm.Err != nil && ctx.Err() != nil:
		return false, ctx.Err()
	case confirm.Err != nil:
		result.Unstable++
		log.Warn().Err(confirm.Err).Int64("contradiction_id", row.ID).
			Msg("Order-swapped audit pass errored; keeping row (not deleted)")
		return false, nil
	case confirm.Verdict != VerdictRemove:
		result.Unstable++
		log.Info().Int64("contradiction_id", row.ID).Str("confirm_verdict", string(confirm.Verdict)).
			Str("confirm_reasoning", confirm.Reasoning).
			Msg("Order-swapped audit pass disagreed; row is unstable, NOT deleting")
		return false, nil
	case confirm.Rule != firstRule:
		result.Unstable++
		log.Info().Int64("contradiction_id", row.ID).
			Str("first_rule", firstRule).Str("confirm_rule", confirm.Rule).
			Str("confirm_reasoning", confirm.Reasoning).
			Msg("Order-swapped audit pass cited a DIFFERENT rule (confabulation signal); NOT deleting")
		return false, nil
	}

	// Signal 3: cross-judge veto. The verifier must ALSO refute it (verdict NO).
	// If the verifier keeps it, it is a likely-real contradiction the auditor
	// mislabeled — do not delete.
	survives, refutation, vErr := verifier.VerifyContradiction(ctx, stmtA, stmtB, row.Reason)
	switch {
	case errors.Is(vErr, llm.ErrUnavailable):
		return false, vErr
	case vErr != nil && ctx.Err() != nil:
		return false, ctx.Err()
	case vErr != nil:
		result.Unstable++
		log.Warn().Err(vErr).Int64("contradiction_id", row.ID).
			Msg("Verifier cross-check errored; keeping row (not deleted)")
		return false, nil
	case survives:
		result.Unstable++
		log.Info().Int64("contradiction_id", row.ID).Str("verifier_reason", refutation).
			Msg("Verifier cross-check KEPT the contradiction (veto); row is unstable, NOT deleting")
		return false, nil
	}

	if err := storer.DismissContradictionAsFalsePositive(ctx, row.ID); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		log.Error().Err(err).Int64("contradiction_id", row.ID).
			Msg("Failed to dismiss contradiction as false positive")
		return false, nil
	}
	result.Deleted++
	log.Warn().Int64("contradiction_id", row.ID).Str("person", row.PersonName).
		Str("rule", firstRule).
		Msg("Dismissed contradiction as false positive (both statement orders agree on the same rule + verifier)")
	return true, nil
}

// recordAuditJobMetrics persists per-verdict counts so the next pipeline tick
// can read GetLatestJobRunTime("contradiction-audit") and decide whether to
// skip. Best-effort: a write failure logs but does not abort the caller —
// missing the timestamp just means the next pipeline run will re-attempt.
func recordAuditJobMetrics(ctx context.Context, storer *storage.PostgresStorer, r *AuditRunResult) {
	jobMetrics := []storage.JobMetric{
		{JobType: "contradiction-audit", Target: "", Metric: "total", Value: float64(r.Total)},
		{JobType: "contradiction-audit", Target: "", Metric: "keep", Value: float64(r.Keep)},
		{JobType: "contradiction-audit", Target: "", Metric: "remove", Value: float64(r.Remove)},
		{JobType: "contradiction-audit", Target: "", Metric: "ambiguous", Value: float64(r.Ambiguous)},
		{JobType: "contradiction-audit", Target: "", Metric: "errors", Value: float64(r.Errors)},
		{JobType: "contradiction-audit", Target: "", Metric: "deleted", Value: float64(r.Deleted)},
		{JobType: "contradiction-audit", Target: "", Metric: "unstable", Value: float64(r.Unstable)},
		{JobType: "contradiction-audit", Target: "", Metric: "cached", Value: float64(r.Cached)},
	}
	if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
		log.Error().Err(err).Msg("Failed to record contradiction-audit job metrics")
	}
}

// formatAuditDate renders a fact date for the audit row ("" when unknown).
func formatAuditDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
