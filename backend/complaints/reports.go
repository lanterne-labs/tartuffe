package complaints

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// ReportStore is the storage of the signed-in users' reports
// (*storage.PostgresStorer).
type ReportStore interface {
	Storer
	ListPendingUserReports(ctx context.Context, limit int) ([]storage.UserReport, error)
	RecordUserReportVerdict(ctx context.Context, id int64, status, aiVerdict, reason string) error
	CountUserReportAttempt(ctx context.Context, id int64, lastError string, maxAttempts int) error
	GetUserReport(ctx context.Context, id int64) (*storage.UserReport, error)
	DecideUserReport(ctx context.Context, id int64, status, reason string) error
}

// MaxReportAttempts is the number of failed judgings after which a report
// goes to review without the judge's advice.
const MaxReportAttempts = 3

// noTally stands in for the Tally form: a report lives in user_reports and
// gets its verdict there.
type noTally struct{}

func (noTally) DeleteSubmission(context.Context, string) error { return nil }

// JudgeReports hands the oldest pending reports, at most limit, to the judge
// and records its advice: the report then waits for a person (review), who
// accepts or refuses it with DecideReport. The judge removes nothing: a
// report is easy to word so that an LLM agrees. A report on a target already
// gone is stale. It returns the number of reports judged. An unavailable LLM
// stops the pass with an error wrapping llm.ErrUnavailable, the report stays
// pending.
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
		out := Triage(ctx, store, noTally{}, judge, []Complaint{complaintOf(r)}, true).Outcomes[0]
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
		status, verdict := storage.ReportReview, string(out.Verdict)
		if out.Stale {
			status, verdict = storage.ReportStale, ""
		}
		if err := store.RecordUserReportVerdict(ctx, r.ID, status, verdict, out.Reason); err != nil {
			return judged, fmt.Errorf("record verdict of report %d: %w", r.ID, err)
		}
		judged++
		log.Info().Int64("report", r.ID).Str("kind", r.TargetKind).Int64("target", r.TargetID).
			Str("status", status).Str("advice", verdict).Str("reason", out.Reason).Msg("User report judged")
	}
	return judged, nil
}

func complaintOf(r storage.UserReport) Complaint {
	c := Complaint{SubmissionID: strconv.FormatInt(r.ID, 10), Kind: Kind(r.TargetKind), Description: r.Reason, SubmittedAt: r.CreatedAt}
	switch r.TargetKind {
	case storage.ReportKindFact:
		c.FactID = r.TargetID
	case storage.ReportKindContradiction:
		c.ContradictionID = r.TargetID
	}
	return c
}

// ErrReportDecided is returned when the report was accepted or found stale
// already.
var ErrReportDecided = errors.New("report already closed")

// DecideReport is a person's decision on a report. Accepting removes its
// target the way the judge used to (a contradiction is dismissed for good,
// a fact goes with its contradictions and the scores are recomputed) and
// publishes the correction, with reason in place of the judge's when it is
// given; a target already gone makes the report stale. Refusing keeps the
// target. A refused report can still be accepted later. It returns the
// report's new status.
func DecideReport(ctx context.Context, store ReportStore, id int64, accept bool, reason string) (string, error) {
	r, err := store.GetUserReport(ctx, id)
	if err != nil {
		return "", err
	}
	if r.Status == storage.ReportAccepted || r.Status == storage.ReportStale {
		return "", fmt.Errorf("report %d is %s: %w", id, r.Status, ErrReportDecided)
	}
	status := storage.ReportRejected
	if accept {
		gone, err := removeTarget(ctx, store, r)
		if err != nil {
			return "", err
		}
		status = storage.ReportAccepted
		if gone {
			status = storage.ReportStale
		}
	}
	if err := store.DecideUserReport(ctx, id, status, reason); err != nil {
		return "", err
	}
	log.Info().Int64("report", id).Str("kind", r.TargetKind).Int64("target", r.TargetID).Str("status", status).Msg("User report decided")
	return status, nil
}

// removeTarget removes the report's target; gone is true when it was
// already gone.
func removeTarget(ctx context.Context, store Storer, r *storage.UserReport) (gone bool, err error) {
	switch r.TargetKind {
	case storage.ReportKindFact:
		if _, err := store.GetFactByID(ctx, r.TargetID); errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		} else if err != nil {
			return false, fmt.Errorf("load fact %d: %w", r.TargetID, err)
		}
		if _, err := store.DeleteFactsAndCleanup(ctx, []int64{r.TargetID}); err != nil {
			return false, fmt.Errorf("delete fact %d: %w", r.TargetID, err)
		}
	case storage.ReportKindContradiction:
		if _, err := store.GetContradictionByID(ctx, r.TargetID); errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		} else if err != nil {
			return false, fmt.Errorf("load contradiction %d: %w", r.TargetID, err)
		}
		if err := store.DismissContradictionAsFalsePositive(ctx, r.TargetID); err != nil {
			return false, fmt.Errorf("dismiss contradiction %d: %w", r.TargetID, err)
		}
	default:
		return false, fmt.Errorf("report %d: unknown kind %q", r.ID, r.TargetKind)
	}
	return false, nil
}
