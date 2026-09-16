package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

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

// RunFactAudit audits every job with `parallelism` concurrent workers (1 =
// sequential), hands each outcome to onResult in completion order (from the
// caller's goroutine, so onResult needs no locking), and returns the tally.
// An auditor error is logged, counted, and still delivered to onResult; an
// onResult error aborts the run.
func RunFactAudit(ctx context.Context, auditor FactAuditor, jobs []FactAuditJob, parallelism int, onResult func(FactAuditOutcome) error) (FactAuditTally, error) {
	if parallelism < 1 {
		parallelism = 1
	}
	var tally FactAuditTally

	feed := make(chan FactAuditJob)
	results := make(chan FactAuditOutcome, parallelism*2)

	var wg sync.WaitGroup
	for w := 0; w < parallelism; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range feed {
				result := auditor.Audit(ctx, j.Input)
				if result.Err != nil {
					log.Error().Err(result.Err).Int64("fact_id", j.FactID).Msg("Fact audit error")
				}
				results <- FactAuditOutcome{FactID: j.FactID, Result: result}
			}
		}()
	}

	go func() {
		for _, j := range jobs {
			feed <- j
		}
		close(feed)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var done atomic.Int64
	for r := range results {
		if r.Result.Err != nil {
			tally.Errors++
		} else {
			switch r.Result.Verdict {
			case FactVerdictKeep:
				tally.Keep++
			case FactVerdictDelete:
				tally.Delete++
				tally.DeleteIDs = append(tally.DeleteIDs, r.FactID)
			case FactVerdictUnsure:
				tally.Unsure++
			}
		}
		if err := onResult(r); err != nil {
			return tally, err
		}
		n := done.Add(1)
		if n%50 == 0 {
			log.Info().
				Int64("audited", n).
				Int("total", len(jobs)).
				Int("keep", tally.Keep).
				Int("delete", tally.Delete).
				Int("unsure", tally.Unsure).
				Int("errors", tally.Errors).
				Msg("Audit progress")
		}
	}
	return tally, nil
}
