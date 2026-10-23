package core

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"digital-mirror/backend/llm"

	"github.com/rs/zerolog/log"
)

// FactAuditor judges whether a stored fact would still be extracted under the
// current rules. OpenAIFactAuditor implements it.
type FactAuditor interface {
	Audit(ctx context.Context, in FactAuditInput) FactAuditResult
}

// FactAuditJob is one fact to audit, keyed by the id the caller needs back.
type FactAuditJob struct {
	FactID int64
	Input  FactAuditInput
}

// FactAuditOutcome is the auditor's result for one job.
type FactAuditOutcome struct {
	FactID int64
	Result FactAuditResult
}

// FactAuditTally counts the verdicts of a RunFactAudit pass.
type FactAuditTally struct {
	Keep      int
	Delete    int
	Unsure    int
	Errors    int
	DeleteIDs []int64 // facts that received a delete verdict, in completion order
}

// FactAuditSubjectLabel picks a single subject entity to show the auditor.
// Most facts have one mention; when several persons are linked we show the
// first (alphabetical) and a "+N more" tail so the LLM still sees a name.
func FactAuditSubjectLabel(personNames []string) string {
	if len(personNames) == 0 {
		return ""
	}
	if len(personNames) == 1 {
		return personNames[0]
	}
	return fmt.Sprintf("%s (+%d more)", personNames[0], len(personNames)-1)
}

// RunFactAudit audits every job, as many at once as the endpoint serves
// (llm.ForEach), hands each outcome to onResult in completion order (one at a
// time, so onResult needs no locking), and returns the tally. An auditor
// error is logged, counted, and still delivered to onResult; an onResult
// error aborts the run. An LLM that cannot serve (llm.ErrUnavailable: every
// seat at its limit, the API cap) stops the run with that error and the tally
// of the facts judged: the facts left were not audited, and a caller that
// took the run as done would never audit them (prod 2026-10-23: September
// 21-30 "audited" in 2 s, every call refused).
func RunFactAudit(ctx context.Context, auditor FactAuditor, jobs []FactAuditJob, onResult func(FactAuditOutcome) error) (FactAuditTally, error) {
	var (
		mu    sync.Mutex
		tally FactAuditTally
		done  int
	)
	err := llm.ForEach(ctx, jobs, func(ctx context.Context, j FactAuditJob) error {
		result := auditor.Audit(ctx, j.Input)
		if errors.Is(result.Err, llm.ErrUnavailable) {
			return result.Err
		}
		if result.Err != nil {
			log.Error().Err(result.Err).Int64("fact_id", j.FactID).Msg("Fact audit error")
		}
		mu.Lock()
		defer mu.Unlock()
		if result.Err != nil {
			tally.Errors++
		} else {
			switch result.Verdict {
			case FactVerdictKeep:
				tally.Keep++
			case FactVerdictDelete:
				tally.Delete++
				tally.DeleteIDs = append(tally.DeleteIDs, j.FactID)
			case FactVerdictUnsure:
				tally.Unsure++
			}
		}
		if err := onResult(FactAuditOutcome{FactID: j.FactID, Result: result}); err != nil {
			return err
		}
		done++
		if done%50 == 0 {
			log.Info().
				Int("audited", done).
				Int("total", len(jobs)).
				Int("keep", tally.Keep).
				Int("delete", tally.Delete).
				Int("unsure", tally.Unsure).
				Int("errors", tally.Errors).
				Msg("Audit progress")
		}
		return nil
	})
	return tally, err
}
