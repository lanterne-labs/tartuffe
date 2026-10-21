package complaints

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// ReportStore is the storage of the signed-in users' reports
// (*storage.PostgresStorer).
type ReportStore interface {
	Storer
	ListPendingUserReports(ctx context.Context, limit int) ([]storage.UserReport, error)
	RecordUserReportVerdict(ctx context.Context, id int64, status, reason string) error
	CountUserReportAttempt(ctx context.Context, id int64, lastError string, maxAttempts int) error
}

// MaxReportAttempts is the number of failed judgings after which a report
// waits for a person.
const MaxReportAttempts = 3

// noTally stands in for the Tally form: a report lives in user_reports and
// gets its verdict there.
type noTally struct{}

func (noTally) DeleteSubmission(context.Context, string) error { return nil }

// JudgeReports hands the oldest pending reports, at most limit, to the judge
// and applies its verdict: a report the judge finds right removes its target
// (a contradiction is dismissed for good, a fact goes with its
// contradictions, and the scores are recomputed); a wrong one keeps it; an
// ambiguous one waits for a person. It returns the number of reports judged.
// An unavailable LLM stops the pass with an error wrapping
// llm.ErrUnavailable, the report stays pending.
func JudgeReports(ctx context.Context, store ReportStore, judge Judge, limit int) (int, error) {
	reports, err := store.ListPendingUserReports(ctx, limit)
	if err != nil {
		return 0, err
	}
	judged := 0
	for _, r := range reports {
		if ctx.Err() != nil {
			return judged, ctx.Err()
		}
		c := Complaint{SubmissionID: strconv.FormatInt(r.ID, 10), Kind: Kind(r.TargetKind), Description: r.Reason, SubmittedAt: r.CreatedAt}
		switch r.TargetKind {
		case storage.ReportKindFact:
			c.FactID = r.TargetID
		case storage.ReportKindContradiction:
			c.ContradictionID = r.TargetID
		}
		out := Triage(ctx, store, noTally{}, judge, []Complaint{c}, false).Outcomes[0]
		if out.Err != nil {
			if errors.Is(out.Err, llm.ErrUnavailable) {
				return judged, out.Err
			}
			log.Error().Err(out.Err).Int64("report", r.ID).Msg("Judging a user report failed")
			if err := store.CountUserReportAttempt(ctx, r.ID, out.Err.Error(), MaxReportAttempts); err != nil {
				return judged, err
			}
			continue
		}
		status := storage.ReportRejected
		switch {
		case out.Stale:
			status = storage.ReportStale
		case out.Verdict == VerdictDeleteTarget && out.TargetDeleted:
			status = storage.ReportAccepted
		case out.Verdict == VerdictAmbiguous:
			status = storage.ReportAmbiguous
		}
		if err := store.RecordUserReportVerdict(ctx, r.ID, status, out.Reason); err != nil {
			return judged, fmt.Errorf("record verdict of report %d: %w", r.ID, err)
		}
		judged++
		log.Info().Int64("report", r.ID).Str("kind", r.TargetKind).Int64("target", r.TargetID).
			Str("status", status).Str("reason", out.Reason).Msg("User report judged")
	}
	return judged, nil
}
