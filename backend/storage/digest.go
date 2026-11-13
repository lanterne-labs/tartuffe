package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/digest"

	"github.com/jackc/pgx/v5"
)

// DigestStore is the storage of the daily digest (backend/digest).
type DigestStore struct{ s *PostgresStorer }

// NewDigestStore reads and records the digests in the application database.
func NewDigestStore(s *PostgresStorer) *DigestStore { return &DigestStore{s: s} }

// DigestCounts counts the facts stored, the contradictions detected, and the
// collection tasks and tweets that failed since a time.
func (d *DigestStore) DigestCounts(ctx context.Context, since time.Time) (digest.Counts, error) {
	var c digest.Counts
	err := d.s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM facts WHERE created_at >= $1),
		(SELECT count(*) FROM contradictions WHERE detected_at >= $1),
		(SELECT count(*) FROM collection_tasks WHERE status = 'failed' AND finished_at >= $1),
		(SELECT count(*) FROM x_posts WHERE status = 'failed' AND created_at >= $1)`, since).
		Scan(&c.Facts, &c.Contradictions, &c.FailedTasks, &c.FailedPosts)
	if err != nil {
		return c, fmt.Errorf("digest counts: %w", err)
	}
	return c, nil
}

// LastJobAt is when a job last recorded a row of job_metrics.
func (d *DigestStore) LastJobAt(ctx context.Context, job string) (time.Time, bool, error) {
	at, err := d.s.GetLatestJobRunTime(ctx, job)
	if err != nil || at == nil {
		return time.Time{}, false, err
	}
	return *at, true, nil
}

// LastDigest is when the last digest went out and the ballots it counted.
func (d *DigestStore) LastDigest(ctx context.Context) (time.Time, int, bool, error) {
	var at time.Time
	var ballots float64
	err := d.s.pool.QueryRow(ctx, `SELECT created_at, value FROM job_metrics WHERE job_type = $1 AND metric = 'ballots' ORDER BY created_at DESC LIMIT 1`, digest.Job).Scan(&at, &ballots)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, 0, false, nil
	}
	if err != nil {
		return time.Time{}, 0, false, fmt.Errorf("last digest: %w", err)
	}
	return at, int(ballots), true, nil
}

// RecordDigest keeps a digest as sent, with what it counted.
func (d *DigestStore) RecordDigest(ctx context.Context, c digest.Counts, ballots int) error {
	return d.s.RecordJobMetrics(ctx, []JobMetric{
		{JobType: digest.Job, Target: "daily", Metric: "facts", Value: float64(c.Facts)},
		{JobType: digest.Job, Target: "daily", Metric: "contradictions", Value: float64(c.Contradictions)},
		{JobType: digest.Job, Target: "daily", Metric: "ballots", Value: float64(ballots)},
	})
}
