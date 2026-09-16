package storage

import (
	"context"

	"fmt"
	"time"
)

// JobMetric represents a single metric data point from a CLI job.
type JobMetric struct {
	JobType string
	Target  string
	Metric  string
	Value   float64
}

// GetLatestJobRunTime returns the timestamp of the most recent job_metrics
// row for the given job_type. Returns (nil, nil) when the job has never run.
// Used by cadence-gated pipeline steps (e.g. the contradiction auditor, which
// runs every N days rather than every daily-pipeline tick).
func (s *PostgresStorer) GetLatestJobRunTime(ctx context.Context, jobType string) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(created_at) FROM job_metrics WHERE job_type = $1`, jobType).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("get latest job run time for %q: %w", jobType, err)
	}
	return t, nil
}

// RecordJobMetrics inserts multiple job metrics in a single batch.
func (s *PostgresStorer) RecordJobMetrics(ctx context.Context, metrics []JobMetric) error {
	if len(metrics) == 0 {
		return nil
	}

	query := "INSERT INTO job_metrics (job_type, target, metric, value) VALUES "
	args := make([]interface{}, 0, len(metrics)*4)
	for i, m := range metrics {
		if i > 0 {
			query += ", "
		}
		base := i * 4
		query += fmt.Sprintf("($%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4)
		args = append(args, m.JobType, m.Target, m.Metric, m.Value)
	}

	_, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("record job metrics: %w", err)
	}
	return nil
}

// PipelineRunSummary is one daily-pipeline run's outcome. The scalar fields are
// supplied by the caller (cmd/pipeline.go); facts_extracted, contradictions_found
// and persons_updated are derived by RecordPipelineRun from the run window so the
// caller does not have to thread counts through every step.
type PipelineRunSummary struct {
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       string // "success" | "warning" | "no_content" | "error"
	Targets      int
	NewsArticles int
	ScrapeErrors int
	AuditChecked int
	AuditFlagged int
	StepErrors   int
	ErrorSummary string
}

// StartPipelineRun inserts the run's row at run START (status='running',
// finished_at NULL) so a killed or hung run leaves a visible trace instead of
// vanishing. Returns the row id, which RecordPipelineRun completes at run end.
func (s *PostgresStorer) StartPipelineRun(ctx context.Context, startedAt time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO pipeline_runs (started_at, status) VALUES ($1, 'running') RETURNING id`,
		startedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("start pipeline run: %w", err)
	}
	return id, nil
}

// RecordPipelineRun completes the pipeline_runs row created by StartPipelineRun.
// facts_extracted, contradictions_found and persons_updated are counted from the
// [StartedAt, FinishedAt] window in the same statement (single round trip)
// rather than passed in. runID == 0 (start-row insert failed) falls back to
// inserting a complete row so the summary is never lost. Best-effort: callers
// should log a failure but not abort the run.
func (s *PostgresStorer) RecordPipelineRun(ctx context.Context, runID int64, sum PipelineRunSummary) error {
	var errSummary *string
	if sum.ErrorSummary != "" {
		errSummary = &sum.ErrorSummary
	}
	var err error
	if runID > 0 {
		_, err = s.pool.Exec(ctx,
			`UPDATE pipeline_runs SET
				finished_at = $2, duration_seconds = $3, status = $4,
				targets = $5, news_articles = $6, scrape_errors = $7,
				facts_extracted = (SELECT count(*) FROM facts WHERE created_at >= $1 AND created_at <= $2),
				contradictions_found = (SELECT count(*) FROM contradictions WHERE created_at >= $1 AND created_at <= $2),
				persons_updated = (SELECT count(DISTINCT entity_id) FROM event_log WHERE topic = 'person_updated' AND created_at >= $1 AND created_at <= $2),
				audit_checked = $8, audit_flagged = $9, step_errors = $10, error_summary = $11
			WHERE id = $12`,
			sum.StartedAt, sum.FinishedAt, int(sum.FinishedAt.Sub(sum.StartedAt).Seconds()), sum.Status,
			sum.Targets, sum.NewsArticles, sum.ScrapeErrors,
			sum.AuditChecked, sum.AuditFlagged, sum.StepErrors, errSummary, runID,
		)
	} else {
		_, err = s.pool.Exec(ctx,
			`INSERT INTO pipeline_runs (
				started_at, finished_at, duration_seconds, status,
				targets, news_articles, scrape_errors,
				facts_extracted, contradictions_found, persons_updated,
				audit_checked, audit_flagged, step_errors, error_summary
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7,
				(SELECT count(*) FROM facts WHERE created_at >= $1 AND created_at <= $2),
				(SELECT count(*) FROM contradictions WHERE created_at >= $1 AND created_at <= $2),
				(SELECT count(DISTINCT entity_id) FROM event_log WHERE topic = 'person_updated' AND created_at >= $1 AND created_at <= $2),
				$8, $9, $10, $11
			)`,
			sum.StartedAt, sum.FinishedAt, int(sum.FinishedAt.Sub(sum.StartedAt).Seconds()), sum.Status,
			sum.Targets, sum.NewsArticles, sum.ScrapeErrors,
			sum.AuditChecked, sum.AuditFlagged, sum.StepErrors, errSummary,
		)
	}
	if err != nil {
		return fmt.Errorf("record pipeline run: %w", err)
	}
	return nil
}

// PipelineHealth summarises pipeline_runs for the watchdog alerter
// (mirror-1ir): when did a run last complete OK, are any 'running' rows stale
// (started >6h ago and never finished — a killed/hung run), and how many runs
// errored in the last 24h.
type PipelineHealth struct {
	LastOKStart  *time.Time // started_at of the newest success/warning run
	StuckRunning int        // status='running' rows started >6h ago
	RecentFailed int        // status='error' rows started in the last 24h
}

// GetPipelineHealth returns the watchdog summary in one round trip.
func (s *PostgresStorer) GetPipelineHealth(ctx context.Context) (PipelineHealth, error) {
	var h PipelineHealth
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT max(started_at) FROM pipeline_runs WHERE status IN ('success','warning','no_content')),
			(SELECT count(*) FROM pipeline_runs WHERE status = 'running' AND started_at < now() - interval '6 hours'),
			(SELECT count(*) FROM pipeline_runs WHERE status = 'error' AND started_at > now() - interval '24 hours')`,
	).Scan(&h.LastOKStart, &h.StuckRunning, &h.RecentFailed)
	if err != nil {
		return h, fmt.Errorf("get pipeline health: %w", err)
	}
	return h, nil
}
