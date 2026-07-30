package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrPipelineJobNotOwned is returned when a worker acts on a job that is no
// longer its own (a reaper requeued it, or it already finished). Callers must
// distinguish it from a transient database error: losing ownership means stop,
// a database blip means retry.
var ErrPipelineJobNotOwned = errors.New("pipeline job is not owned by this worker")

// Pipeline job statuses.
const (
	PipelineJobQueued   = "queued"
	PipelineJobRunning  = "running"
	PipelineJobSuccess  = "success"
	PipelineJobError    = "error"
	PipelineJobCanceled = "canceled"
)

// PipelineJob is one queued (or past) pipeline execution. Config carries the
// caller's serialized pipeline configuration — the storage layer treats it as
// opaque JSON; only the pipeline runner interprets it.
type PipelineJob struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Config      json.RawMessage `json:"config"`
	Status      string          `json:"status"`
	Priority    int             `json:"priority"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	WorkerID    *string         `json:"worker_id,omitempty"`
	QueuedAt    time.Time       `json:"queued_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	HeartbeatAt *time.Time      `json:"heartbeat_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	RunID       *int64          `json:"run_id,omitempty"`
	Error       *string         `json:"error,omitempty"`
}

// pipelineJobCols is the column list every pipeline-job query returns, in the
// order scanPipelineJob expects.
const pipelineJobCols = `id, name, config, status, priority, attempts, max_attempts,
	worker_id, queued_at, started_at, heartbeat_at, finished_at, run_id, error`

func scanPipelineJob(row pgx.Row) (*PipelineJob, error) {
	var j PipelineJob
	err := row.Scan(&j.ID, &j.Name, &j.Config, &j.Status, &j.Priority, &j.Attempts, &j.MaxAttempts,
		&j.WorkerID, &j.QueuedAt, &j.StartedAt, &j.HeartbeatAt, &j.FinishedAt, &j.RunID, &j.Error)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// EnqueuePipelineJob queues a pipeline run. Names are unique among non-terminal
// jobs: if a job with the same name is already queued or running, nothing is
// inserted and the existing job is returned with created=false (this is what
// keeps the cron's nightly enqueue from stacking onto a run still in flight).
func (s *PostgresStorer) EnqueuePipelineJob(ctx context.Context, name string, config []byte, priority int) (job *PipelineJob, created bool, err error) {
	if name == "" {
		return nil, false, fmt.Errorf("pipeline job name is required")
	}
	if len(config) == 0 {
		config = []byte("{}")
	}

	// Single round trip: insert, and if the partial unique index rejects it, fall
	// through to the already-active row with the same name.
	query := `
		WITH ins AS (
			INSERT INTO pipeline_jobs (name, config, priority)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING
			RETURNING ` + pipelineJobCols + `
		)
		SELECT ` + pipelineJobCols + `, true AS created FROM ins
		UNION ALL
		SELECT ` + pipelineJobCols + `, false AS created
		FROM pipeline_jobs
		WHERE name = $1 AND status IN ('queued', 'running')
		  AND NOT EXISTS (SELECT 1 FROM ins)`

	var j PipelineJob
	err = s.pool.QueryRow(ctx, query, name, config, priority).Scan(
		&j.ID, &j.Name, &j.Config, &j.Status, &j.Priority, &j.Attempts, &j.MaxAttempts,
		&j.WorkerID, &j.QueuedAt, &j.StartedAt, &j.HeartbeatAt, &j.FinishedAt, &j.RunID, &j.Error,
		&created)
	if err != nil {
		return nil, false, fmt.Errorf("enqueue pipeline job %q: %w", name, err)
	}
	return &j, created, nil
}

// ClaimNextPipelineJob atomically takes the highest-priority queued job and
// marks it running for workerID. Returns (nil, nil) when the queue is empty.
// FOR UPDATE SKIP LOCKED makes the claim safe if a second worker ever exists.
func (s *PostgresStorer) ClaimNextPipelineJob(ctx context.Context, workerID string) (*PipelineJob, error) {
	query := `
		UPDATE pipeline_jobs SET
			status = 'running',
			started_at = now(),
			heartbeat_at = now(),
			finished_at = NULL,
			error = NULL,
			attempts = attempts + 1,
			worker_id = $1
		WHERE id = (
			SELECT id FROM pipeline_jobs
			WHERE status = 'queued'
			ORDER BY priority ASC, queued_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING ` + pipelineJobCols

	job, err := scanPipelineJob(s.pool.QueryRow(ctx, query, workerID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim pipeline job: %w", err)
	}
	return job, nil
}

// HeartbeatPipelineJob refreshes the liveness stamp of a running job so
// ReapStalePipelineJobs can tell a crashed worker from a slow one.
func (s *PostgresStorer) HeartbeatPipelineJob(ctx context.Context, id int64, workerID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE pipeline_jobs SET heartbeat_at = now()
		 WHERE id = $1 AND worker_id = $2 AND status = 'running'`, id, workerID)
	if err != nil {
		return fmt.Errorf("heartbeat pipeline job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("heartbeat pipeline job %d (worker %s): %w", id, workerID, ErrPipelineJobNotOwned)
	}
	return nil
}

// FinishPipelineJob closes out a job with a terminal status. runID links the job
// to its pipeline_runs summary row (nil when the run never got that far).
func (s *PostgresStorer) FinishPipelineJob(ctx context.Context, id int64, status string, runID *int64, errMsg string) error {
	switch status {
	case PipelineJobSuccess, PipelineJobError, PipelineJobCanceled:
	default:
		return fmt.Errorf("finish pipeline job %d: invalid terminal status %q", id, status)
	}
	var errPtr *string
	if errMsg != "" {
		errPtr = &errMsg
	}
	var runIDArg *int64
	if runID != nil && *runID > 0 {
		runIDArg = runID
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE pipeline_jobs SET status = $2, finished_at = now(), run_id = $3, error = $4, worker_id = NULL
		 WHERE id = $1`, id, status, runIDArg, errPtr)
	if err != nil {
		return fmt.Errorf("finish pipeline job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("finish pipeline job %d: no such job", id)
	}
	return nil
}

// ReleasePipelineJob puts a running job back on the queue without consuming an
// attempt. Used on graceful shutdown (the host redeploy) so an interrupted run
// resumes on the new instance instead of being lost until the next cron tick.
func (s *PostgresStorer) ReleasePipelineJob(ctx context.Context, id int64, workerID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE pipeline_jobs SET status = 'queued', started_at = NULL, heartbeat_at = NULL,
			worker_id = NULL, attempts = GREATEST(attempts - 1, 0)
		 WHERE id = $1 AND worker_id = $2 AND status = 'running'`, id, workerID)
	if err != nil {
		return fmt.Errorf("release pipeline job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("release pipeline job %d (worker %s): %w", id, workerID, ErrPipelineJobNotOwned)
	}
	return nil
}

// ReapStalePipelineJobs recovers jobs whose worker died mid-run (no heartbeat
// for staleAfter). A job under its attempt budget goes back to 'queued';
// one that has exhausted it is failed so a crash-looping config can't spin
// forever. Returns how many were requeued and how many were failed.
func (s *PostgresStorer) ReapStalePipelineJobs(ctx context.Context, staleAfter time.Duration) (requeued, failed int, err error) {
	query := `
		WITH stale AS (
			SELECT id, attempts, max_attempts FROM pipeline_jobs
			WHERE status = 'running' AND heartbeat_at < now() - $1::interval
			FOR UPDATE SKIP LOCKED
		)
		UPDATE pipeline_jobs j SET
			status = CASE WHEN s.attempts < s.max_attempts THEN 'queued' ELSE 'error' END,
			worker_id = NULL,
			started_at = CASE WHEN s.attempts < s.max_attempts THEN NULL ELSE j.started_at END,
			heartbeat_at = NULL,
			finished_at = CASE WHEN s.attempts < s.max_attempts THEN NULL ELSE now() END,
			error = CASE WHEN s.attempts < s.max_attempts THEN NULL
			             ELSE 'worker died mid-run (no heartbeat) and attempts exhausted' END
		FROM stale s
		WHERE j.id = s.id
		RETURNING j.status`

	rows, err := s.pool.Query(ctx, query, staleAfter.String())
	if err != nil {
		return 0, 0, fmt.Errorf("reap stale pipeline jobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return requeued, failed, fmt.Errorf("reap stale pipeline jobs: scan: %w", err)
		}
		if status == PipelineJobQueued {
			requeued++
		} else {
			failed++
		}
	}
	return requeued, failed, rows.Err()
}

// CancelPipelineJob cancels a job that has not started yet. A running job is not
// cancelable from here — stop the worker instead.
func (s *PostgresStorer) CancelPipelineJob(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE pipeline_jobs SET status = 'canceled', finished_at = now()
		 WHERE id = $1 AND status = 'queued'`, id)
	if err != nil {
		return fmt.Errorf("cancel pipeline job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cancel pipeline job %d: not found or no longer queued", id)
	}
	return nil
}

// GetPipelineJob returns one job by id.
func (s *PostgresStorer) GetPipelineJob(ctx context.Context, id int64) (*PipelineJob, error) {
	job, err := scanPipelineJob(s.pool.QueryRow(ctx,
		`SELECT `+pipelineJobCols+` FROM pipeline_jobs WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("pipeline job %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get pipeline job %d: %w", id, err)
	}
	return job, nil
}

// ListPipelineJobs returns jobs newest-first, optionally filtered by status
// (empty = all). Pending jobs are listed in claim order (priority, queued_at).
func (s *PostgresStorer) ListPipelineJobs(ctx context.Context, status string, limit int) ([]PipelineJob, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT ` + pipelineJobCols + ` FROM pipeline_jobs
		WHERE ($1 = '' OR status = $1)
		ORDER BY (status = 'queued') DESC,
		         CASE WHEN status = 'queued' THEN priority END ASC,
		         CASE WHEN status = 'queued' THEN queued_at END ASC,
		         id DESC
		LIMIT $2`
	rows, err := s.pool.Query(ctx, query, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list pipeline jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PipelineJob
	for rows.Next() {
		job, err := scanPipelineJob(rows)
		if err != nil {
			return nil, fmt.Errorf("list pipeline jobs: scan: %w", err)
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}
