package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Collection task statuses.
const (
	TaskPending   = "pending"
	TaskRunning   = "running"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// CollectionTask asks one plugin for the facts of one person over the period
// [Since, Until), whole days in UTC. `dm collect add` creates tasks and
// `dm worker run` claims and runs them.
type CollectionTask struct {
	ID          int64
	PersonID    int64
	PersonName  string
	EntityType  string
	Plugin      string
	Since       time.Time
	Until       time.Time
	Status      string
	Priority    int
	Attempts    int
	NotBefore   time.Time
	LastError   string
	RequestedBy string
	Operation   string
	Items       int
	Facts       int
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// NewCollectionTask is the request of one task.
type NewCollectionTask struct {
	PersonID    int64
	Plugin      string
	Since       time.Time
	Until       time.Time
	Priority    int
	RequestedBy string
	// Operation names the batch the task belongs to ("" for none).
	Operation string
}

const collectionTaskColumns = `t.id, t.person_id, p.name, COALESCE(p.entity_type, 'human'), t.plugin, t.since, t.until,
	t.status, t.priority, t.attempts, t.not_before, COALESCE(t.last_error, ''), COALESCE(t.requested_by, ''), COALESCE(t.operation, ''),
	t.items, t.facts, t.created_at, t.started_at, t.finished_at`

func scanCollectionTask(row pgx.Row) (*CollectionTask, error) {
	var t CollectionTask
	err := row.Scan(&t.ID, &t.PersonID, &t.PersonName, &t.EntityType, &t.Plugin, &t.Since, &t.Until,
		&t.Status, &t.Priority, &t.Attempts, &t.NotBefore, &t.LastError, &t.RequestedBy, &t.Operation,
		&t.Items, &t.Facts, &t.CreatedAt, &t.StartedAt, &t.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// AddCollectionTasks creates the tasks and returns how many were new: a task
// equal to an open one (same person, plugin and period) is not created again.
func (s *PostgresStorer) AddCollectionTasks(ctx context.Context, tasks []NewCollectionTask) (int, error) {
	added := 0
	for _, t := range tasks {
		if !t.Since.Before(t.Until) {
			return added, fmt.Errorf("collection task: since %s is not before until %s", t.Since.Format("2006-01-02"), t.Until.Format("2006-01-02"))
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO collection_tasks (person_id, plugin, since, until, priority, requested_by, operation)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''))
			ON CONFLICT DO NOTHING`,
			t.PersonID, t.Plugin, t.Since, t.Until, t.Priority, t.RequestedBy, t.Operation)
		if err != nil {
			return added, fmt.Errorf("adding collection task: %w", err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// ClaimCollectionTask takes the next task to run: the pending task of the
// highest priority, oldest first, whose not_before has passed, or a running
// task whose heartbeat is older than lease (its worker died). The task comes
// back running, with one more attempt; nil when there is none. Two workers
// never claim the same task (FOR UPDATE SKIP LOCKED).
func (s *PostgresStorer) ClaimCollectionTask(ctx context.Context, lease time.Duration) (*CollectionTask, error) {
	row := s.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM collection_tasks
			WHERE (status = 'pending' AND not_before <= now())
			   OR (status = 'running' AND heartbeat_at < now() - $1::interval)
			ORDER BY priority DESC, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		), t AS (
			UPDATE collection_tasks c
			SET status = 'running', attempts = c.attempts + 1, started_at = now(), heartbeat_at = now(), finished_at = NULL
			FROM next WHERE c.id = next.id
			RETURNING c.*
		)
		SELECT `+collectionTaskColumns+` FROM t JOIN persons p ON p.id = t.person_id`,
		lease)
	t, err := scanCollectionTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claiming collection task: %w", err)
	}
	return t, nil
}

// HeartbeatCollectionTask renews the lease of a running task. It reports
// false when the task is no longer running: cancelled, or claimed by another
// worker after its lease expired.
func (s *PostgresStorer) HeartbeatCollectionTask(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE collection_tasks SET heartbeat_at = now() WHERE id = $1 AND status = 'running'`, id)
	if err != nil {
		return false, fmt.Errorf("heartbeat of collection task %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// errTaskNotRunning is the error of a write to a task that is no longer
// running: cancelled, or claimed by another worker after its lease expired.
var errTaskNotRunning = errors.New("the task is no longer running (cancelled, or claimed by another worker)")

// FinishCollectionTask closes a running task as done or failed, with its
// counts and its last_error: the error of a failure, or the notes of a done
// task (a person without a handle). A task that is no longer running is left
// as it is, with an error.
func (s *PostgresStorer) FinishCollectionTask(ctx context.Context, id int64, status, lastError string, items, facts int) error {
	if status != TaskDone && status != TaskFailed {
		return fmt.Errorf("finishing collection task %d: status %q is not done or failed", id, status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE collection_tasks
		SET status = $2, last_error = NULLIF($3, ''), items = items + $4, facts = facts + $5, finished_at = now()
		WHERE id = $1 AND status = 'running'`, id, status, lastError, items, facts)
	if err != nil {
		return fmt.Errorf("finishing collection task %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("finishing collection task %d: %w", id, errTaskNotRunning)
	}
	return nil
}

// ReleaseCollectionTask puts a running task back to pending, to be claimed
// again at notBefore. The counts of the attempt are kept and its error is
// recorded. A failed attempt counts (countAttempt): the task is retried after
// a backoff. A stop of the worker or a wait for the LLM is not an attempt of
// the task: the claim's count is undone. A task that is no longer running is
// left as it is, with an error.
func (s *PostgresStorer) ReleaseCollectionTask(ctx context.Context, id int64, lastError string, notBefore time.Time, items, facts int, countAttempt bool) error {
	undo := 0
	if !countAttempt {
		undo = 1
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE collection_tasks
		SET status = 'pending', last_error = NULLIF($2, ''), not_before = $3, items = items + $4, facts = facts + $5,
		    attempts = attempts - $6
		WHERE id = $1 AND status = 'running'`, id, lastError, notBefore, items, facts, undo)
	if err != nil {
		return fmt.Errorf("releasing collection task %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("releasing collection task %d: %w", id, errTaskNotRunning)
	}
	return nil
}

// CancelCollectionTasks cancels the open tasks of the ids and returns how many
// it cancelled. A running task is cancelled too: its worker sees it at the
// next heartbeat and stops the attempt.
func (s *PostgresStorer) CancelCollectionTasks(ctx context.Context, ids []int64) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE collection_tasks SET status = 'cancelled', finished_at = now()
		WHERE id = ANY($1) AND status IN ('pending', 'running')`, ids)
	if err != nil {
		return 0, fmt.Errorf("cancelling collection tasks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListCollectionTasks lists tasks, newest first; status "" lists every status.
func (s *PostgresStorer) ListCollectionTasks(ctx context.Context, status string, limit int) ([]CollectionTask, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+collectionTaskColumns+`
		FROM collection_tasks t JOIN persons p ON p.id = t.person_id
		WHERE $1 = '' OR t.status = $1
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $2`, status, limit)
	if err != nil {
		return nil, fmt.Errorf("listing collection tasks: %w", err)
	}
	defer rows.Close()
	var out []CollectionTask
	for rows.Next() {
		t, err := scanCollectionTask(rows)
		if err != nil {
			return nil, fmt.Errorf("listing collection tasks: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// CountCollectionTasks returns the number of tasks per status.
func (s *PostgresStorer) CountCollectionTasks(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, COUNT(*) FROM collection_tasks GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("counting collection tasks: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("counting collection tasks: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}
