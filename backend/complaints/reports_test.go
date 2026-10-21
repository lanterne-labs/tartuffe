package complaints

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"digital-mirror/backend/core"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memReports is a report store with one fact (1) and one contradiction (2).
type memReports struct {
	reports        []storage.UserReport
	factGone       bool
	contradictGone bool
	dismissed      []int64
	deletedFacts   []int64
	attempts       map[int64]int
}

func (m *memReports) GetFactByID(_ context.Context, id int64) (*core.FactForDedup, error) {
	if id != 1 || m.factGone {
		return nil, pgx.ErrNoRows
	}
	return &core.FactForDedup{ID: 1, NormalizedFactText: "Il a voté la réforme."}, nil
}
func (m *memReports) GetFactSourceURLs(context.Context, int64) ([]string, error) {
	return []string{"http://ex/1"}, nil
}
func (m *memReports) DeleteFactsAndCleanup(_ context.Context, ids []int64) (storage.FactDeletionCleanupResult, error) {
	m.deletedFacts = append(m.deletedFacts, ids...)
	m.factGone = true
	return storage.FactDeletionCleanupResult{FactsDeleted: len(ids)}, nil
}
func (m *memReports) GetContradictionByID(_ context.Context, id int64) (*storage.ContradictionWithFacts, error) {
	if id != 2 || m.contradictGone {
		return nil, pgx.ErrNoRows
	}
	return &storage.ContradictionWithFacts{ID: 2, FactAText: "60 ans", FactBText: "65 ans"}, nil
}
func (m *memReports) DismissContradictionAsFalsePositive(_ context.Context, id int64) error {
	m.dismissed = append(m.dismissed, id)
	m.contradictGone = true
	return nil
}
func (m *memReports) GetStanceDocumentByID(context.Context, int64) (*core.StanceDocument, error) {
	return nil, pgx.ErrNoRows
}
func (m *memReports) GetFactsForStance(context.Context, int64) ([]core.StanceFactInfo, error) {
	return nil, nil
}
func (m *memReports) DeleteStanceDocument(context.Context, int64) error { return nil }

func (m *memReports) ListPendingUserReports(_ context.Context, limit int) ([]storage.UserReport, error) {
	var out []storage.UserReport
	for _, r := range m.reports {
		if r.Status == storage.ReportPending && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memReports) RecordUserReportVerdict(_ context.Context, id int64, status, reason string) error {
	for i := range m.reports {
		if m.reports[i].ID == id {
			m.reports[i].Status, m.reports[i].VerdictReason = status, reason
		}
	}
	return nil
}
func (m *memReports) CountUserReportAttempt(_ context.Context, id int64, _ string, max int) error {
	if m.attempts == nil {
		m.attempts = map[int64]int{}
	}
	m.attempts[id]++
	if m.attempts[id] >= max {
		return m.RecordUserReportVerdict(context.Background(), id, storage.ReportAmbiguous, "judge failed")
	}
	return nil
}

func (m *memReports) status(id int64) string {
	for _, r := range m.reports {
		if r.ID == id {
			return r.Status
		}
	}
	return ""
}

// fakeJudge answers by the words of the prompt.
type fakeJudge struct {
	verdicts map[string]Verdict // a word of the report -> verdict
	err      error
	prompts  []string
}

func (j *fakeJudge) Judge(_ context.Context, prompt string) (VerdictResult, error) {
	j.prompts = append(j.prompts, prompt)
	if j.err != nil {
		return VerdictResult{}, j.err
	}
	for word, v := range j.verdicts {
		if strings.Contains(prompt, word) {
			return VerdictResult{Verdict: v, Reason: "because " + word}, nil
		}
	}
	return VerdictResult{Verdict: VerdictAmbiguous, Reason: "unclear"}, nil
}

func report(id int64, kind string, target int64, reason string) storage.UserReport {
	return storage.UserReport{ID: id, TargetKind: kind, TargetID: target, Reason: reason, Status: storage.ReportPending}
}

// TestJudgeReports: the judge sees the target and the report; a report
// found right removes its target the safe way, a wrong one keeps it, and a
// report on a target already gone is stale.
func TestJudgeReports(t *testing.T) {
	m := &memReports{reports: []storage.UserReport{
		report(1, storage.ReportKindContradiction, 2, "RIGHT: two different laws"),
		report(2, storage.ReportKindFact, 1, "WRONG: he never voted"),
		report(3, storage.ReportKindContradiction, 2, "RIGHT: same as above"),
		report(4, storage.ReportKindFact, 1, "MAYBE: unclear"),
	}}
	j := &fakeJudge{verdicts: map[string]Verdict{"RIGHT": VerdictDeleteTarget, "WRONG": VerdictDismissComplaint}}
	n, err := JudgeReports(context.Background(), m, j, 10)
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	assert.Equal(t, storage.ReportAccepted, m.status(1))
	assert.Equal(t, []int64{2}, m.dismissed, "the contradiction is dismissed for good")
	assert.Contains(t, j.prompts[0], "60 ans")
	assert.Contains(t, j.prompts[0], "RIGHT: two different laws")

	assert.Equal(t, storage.ReportRejected, m.status(2))
	assert.Empty(t, m.deletedFacts)
	assert.Equal(t, storage.ReportStale, m.status(3), "the target went with report 1")
	assert.Equal(t, storage.ReportAmbiguous, m.status(4))
	assert.Len(t, j.prompts, 3, "a stale report costs no call")

	m.reports = append(m.reports, report(5, storage.ReportKindFact, 1, "RIGHT: misattributed"))
	_, err = JudgeReports(context.Background(), m, j, 10)
	require.NoError(t, err)
	assert.Equal(t, storage.ReportAccepted, m.status(5))
	assert.Equal(t, []int64{1}, m.deletedFacts, "the fact goes with its cleanup")
}

// TestJudgeReportsFailures: an unavailable LLM stops the pass and leaves
// the report pending; another failure counts an attempt.
func TestJudgeReportsFailures(t *testing.T) {
	m := &memReports{reports: []storage.UserReport{report(1, storage.ReportKindFact, 1, "RIGHT"), report(2, storage.ReportKindFact, 1, "RIGHT")}}
	_, err := JudgeReports(context.Background(), m, &fakeJudge{err: fmt.Errorf("call: %w", llm.ErrUnavailable)}, 10)
	assert.True(t, errors.Is(err, llm.ErrUnavailable))
	assert.Equal(t, storage.ReportPending, m.status(1))
	assert.Empty(t, m.attempts)

	for range MaxReportAttempts {
		_, err = JudgeReports(context.Background(), m, &fakeJudge{err: errors.New("bad json")}, 1)
		require.NoError(t, err)
	}
	assert.Equal(t, storage.ReportAmbiguous, m.status(1), "after the attempts a person decides")
	assert.Equal(t, storage.ReportPending, m.status(2))
}
