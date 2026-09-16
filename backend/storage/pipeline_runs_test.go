package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordPipelineRun verifies that RecordPipelineRun persists the scalar
// summary fields verbatim and derives facts_extracted / contradictions_found /
// persons_updated from the [StartedAt, FinishedAt] window. Facts and the
// contradiction are created inside the window; a person_updated event is emitted
// by RecordContradictionImmediate. The window is anchored at test start so
// older event_log rows from previous tests are excluded.
func TestRecordPipelineRun(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	if _, err := storer.pool.Exec(ctx, "DELETE FROM pipeline_runs"); err != nil {
		t.Skipf("pipeline_runs table not present (run migrations): %v", err)
	}

	windowStart := time.Now().Add(-time.Second)

	pid, err := storer.FindOrInsertPersonWithType(ctx, "pipeline run subject", "human")
	require.NoError(t, err)

	faID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity: "pipeline run subject", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "subject promised to lower taxes", EmbeddingText: "promised to lower taxes",
		Embedding: mockEmbedding(), CanonicalVerb: "PROMISED", SourceURL: "http://example.com/a", SourceType: "web",
	})
	require.NoError(t, err)
	fbID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity: "pipeline run subject", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "subject voted to raise taxes", EmbeddingText: "voted to raise taxes",
		Embedding: mockEmbedding(), CanonicalVerb: "VOTED", SourceURL: "http://example.com/b", SourceType: "web",
	})
	require.NoError(t, err)

	// fact_a_id < fact_b_id is enforced by a CHECK constraint.
	aID, bID := faID, fbID
	if aID > bID {
		aID, bID = bID, aID
	}
	_, _, err = storer.RecordContradictionImmediate(ctx, pid, aID, bID, "raison", "reason")
	require.NoError(t, err)

	// The run row now exists from the START (status='running') so killed runs
	// leave a trace; RecordPipelineRun completes it by id.
	runID, err := storer.StartPipelineRun(ctx, windowStart)
	require.NoError(t, err)
	var midStatus string
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT status FROM pipeline_runs WHERE id = $1`, runID).Scan(&midStatus))
	assert.Equal(t, "running", midStatus, "start row must be visible as running mid-run")

	finishedAt := time.Now().Add(time.Second)
	err = storer.RecordPipelineRun(ctx, runID, PipelineRunSummary{
		StartedAt: windowStart, FinishedAt: finishedAt, Status: "success",
		Targets: 5, NewsArticles: 10, ScrapeErrors: 1,
		AuditChecked: 3, AuditFlagged: 1, StepErrors: 0,
	})
	require.NoError(t, err)

	var (
		status                                 string
		facts, contras, persons, dur           int
		targets, news, scrape, audChk, audFlag int
		stepErrs                               int
		errSummary                             *string
	)
	err = storer.pool.QueryRow(ctx, `
		SELECT status, facts_extracted, contradictions_found, persons_updated, duration_seconds,
		       targets, news_articles, scrape_errors, audit_checked, audit_flagged, step_errors, error_summary
		FROM pipeline_runs ORDER BY id DESC LIMIT 1`).
		Scan(&status, &facts, &contras, &persons, &dur, &targets, &news, &scrape, &audChk, &audFlag, &stepErrs, &errSummary)
	require.NoError(t, err)

	assert.Equal(t, "success", status)
	assert.Equal(t, 5, targets)
	assert.Equal(t, 10, news)
	assert.Equal(t, 1, scrape)
	assert.Equal(t, 3, audChk)
	assert.Equal(t, 1, audFlag)
	assert.Equal(t, 0, stepErrs)
	assert.Nil(t, errSummary, "empty ErrorSummary should persist as NULL")
	assert.GreaterOrEqual(t, dur, 1, "duration_seconds spans the window")

	// Window-derived counts: the two facts and one contradiction we just created,
	// plus the person_updated event RecordContradictionImmediate emits.
	assert.GreaterOrEqual(t, facts, 2, "facts_extracted counts in-window facts")
	assert.GreaterOrEqual(t, contras, 1, "contradictions_found counts in-window contradictions")
	assert.GreaterOrEqual(t, persons, 1, "persons_updated counts distinct in-window person_updated events")
}
