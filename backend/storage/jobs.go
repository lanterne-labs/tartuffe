package storage

import (
	"context"
	"errors"

	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// LastJobMetric returns the newest row of (jobType, metric): when it was
// recorded and its value. ok is false when none was recorded.
func (s *PostgresStorer) LastJobMetric(ctx context.Context, jobType, metric string) (at time.Time, value float64, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT created_at, value FROM job_metrics
		WHERE job_type = $1 AND metric = $2
		ORDER BY created_at DESC LIMIT 1`, jobType, metric).Scan(&at, &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, 0, false, nil
	}
	if err != nil {
		return time.Time{}, 0, false, fmt.Errorf("last %s %s: %w", jobType, metric, err)
	}
	return at, value, true, nil
}
