// Package worker runs the collection tasks: `dm collect add` asks for the
// facts of one person over one period from one plugin, and the worker claims
// the tasks one at a time and runs them as soon as it is free. A period a
// plugin has fetched is never fetched again: the plugin's own fetch log skips
// it, and a task closes as done only when the log covers its whole period.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Options tune the loop.
type Options struct {
	// Poll is the pause when no task is pending.
	Poll time.Duration
	// Lease is the heartbeat age after which a running task is claimed again
	// (its worker died).
	Lease time.Duration
	// MaxAttempts is the number of attempts before a task fails for good.
	MaxAttempts int
	// Backoff is the pause before the retry of a failed attempt, multiplied by
	// the attempts made.
	Backoff time.Duration
	// MaxLLMWait is how long the worker waits for an unavailable LLM before it
	// polls again.
	MaxLLMWait time.Duration
}

// DefaultOptions are the options of `dm worker run`.
var DefaultOptions = Options{
	Poll:        30 * time.Second,
	Lease:       10 * time.Minute,
	MaxAttempts: 3,
	Backoff:     10 * time.Minute,
	MaxLLMWait:  time.Hour,
}

// Outcome is what one attempt of a task collected.
type Outcome struct {
	Items int
	Facts int
	// Covered reports whether the plugin's fetch log covers the whole period
	// after the attempt: the task is done only then.
	Covered bool
	// Details are the per-item failures of the attempt (a search quota, a
	// page that would not scrape).
	Details []string
}

// Runner runs one attempt of a task. PluginRunner is the production one.
type Runner interface {
	Run(ctx context.Context, task storage.CollectionTask) (Outcome, error)
}

// Tasks is the storage the worker needs (*storage.PostgresStorer).
type Tasks interface {
	ClaimCollectionTask(ctx context.Context, lease time.Duration) (*storage.CollectionTask, error)
	HeartbeatCollectionTask(ctx context.Context, id int64) (running bool, err error)
	FinishCollectionTask(ctx context.Context, id int64, status, lastError string, items, facts int) error
	ReleaseCollectionTask(ctx context.Context, id int64, lastError string, notBefore time.Time, items, facts int, countAttempt bool) error
}

// Validate checks the options.
func (o Options) Validate() error {
	if o.Poll <= 0 || o.Lease <= 0 || o.MaxAttempts <= 0 || o.Backoff < 0 || o.MaxLLMWait <= 0 {
		return fmt.Errorf("worker options: poll, lease, max attempts and LLM wait must be positive, backoff zero or more (got %+v)", o)
	}
	return nil
}

// Run claims and runs tasks until ctx ends, and returns ctx.Err(). A task the
// worker is on when ctx ends goes back to pending at once.
func Run(ctx context.Context, tasks Tasks, runner Runner, opts Options) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	log.Info().Dur("poll", opts.Poll).Dur("lease", opts.Lease).Msg("Worker started")
	for {
		task, err := tasks.ClaimCollectionTask(ctx, opts.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error().Err(err).Msg("Worker: claiming a task failed; retrying after the poll pause")
		} else if task != nil {
			err = runOne(ctx, tasks, runner, opts, *task)
			if err != nil && !errors.Is(err, errWaited) && ctx.Err() == nil {
				log.Error().Err(err).Int64("task", task.ID).Msg("Worker: recording the attempt failed; the lease of the task expires")
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if task != nil && err == nil {
			continue // the next task at once
		}
		select {
		case <-time.After(opts.Poll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// errWaited marks an attempt that stopped on an unavailable LLM: the caller
// pauses before the next claim.
var errWaited = errors.New("waited for the LLM")

// runOne runs one attempt of task and records its outcome. It returns an
// error when the loop must pause before the next claim.
func runOne(ctx context.Context, tasks Tasks, runner Runner, opts Options, task storage.CollectionTask) error {
	logger := log.With().Int64("task", task.ID).Str("plugin", task.Plugin).Str("person", task.PersonName).
		Str("since", task.Since.Format("2006-01-02")).Str("until", task.Until.Format("2006-01-02")).Int("attempt", task.Attempts).Logger()
	logger.Info().Msg("Worker: task started")

	// The attempt runs on its own context: the heartbeat cancels it when the
	// task is no longer running (cancelled, or claimed by another worker).
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	go heartbeat(hbCtx, cancelRun, tasks, task.ID, opts.Lease/3)
	start := time.Now()
	out, err := runner.Run(runCtx, task)
	stopHeartbeat()
	// The bookkeeping outlives a stop of the worker.
	wctx := context.WithoutCancel(ctx)
	elapsed := time.Since(start).Round(time.Second)

	switch {
	case ctx.Err() != nil:
		logger.Warn().Dur("elapsed", elapsed).Msg("Worker: stopped; the task goes back to pending")
		return release(wctx, tasks, task, "worker stopped", time.Now(), out, false)
	case runCtx.Err() != nil:
		// Cancelled, or taken over: the row is not this attempt's any more.
		logger.Warn().Dur("elapsed", elapsed).Msg("Worker: the task is no longer running (cancelled); attempt dropped")
		metrics.CollectionTaskAttemptsTotal.WithLabelValues(task.Plugin, storage.TaskCancelled).Inc()
		return nil
	case errors.Is(err, llm.ErrUnavailable):
		until := time.Now().Add(opts.MaxLLMWait)
		var ue *llm.UnavailableError
		if errors.As(err, &ue) && ue.RetryAt.After(until) {
			// A reset too far away to wait for: the task waits for it instead.
			logger.Warn().Err(err).Time("retry_at", ue.RetryAt).Dur("elapsed", elapsed).
				Msg("Worker: LLM unavailable until its reset; the task waits for it")
			if rErr := release(wctx, tasks, task, err.Error(), ue.RetryAt, out, false); rErr != nil {
				return rErr
			}
			return errWaited
		}
		logger.Warn().Err(err).Dur("elapsed", elapsed).Msg("Worker: LLM unavailable; the task goes back to pending and the worker waits")
		if rErr := release(wctx, tasks, task, err.Error(), time.Now(), out, false); rErr != nil {
			return rErr
		}
		if back, _ := llm.WaitForEndpoint(ctx, err, until); back {
			return nil
		}
		return errWaited
	case err == nil && out.Covered:
		logger.Info().Dur("elapsed", elapsed).Int("items", out.Items).Int("facts", out.Facts).Msg("Worker: task done")
		metrics.CollectionTaskAttemptsTotal.WithLabelValues(task.Plugin, storage.TaskDone).Inc()
		return tasks.FinishCollectionTask(wctx, task.ID, storage.TaskDone, "", out.Items, out.Facts)
	}
	// A failure of the attempt, or a period the fetch log does not cover (a
	// search that failed, a page that would not scrape).
	msg := "period not fully fetched"
	if err != nil {
		msg = err.Error()
	}
	if len(out.Details) > 0 {
		msg += ": " + strings.Join(out.Details, "; ")
	}
	if task.Attempts >= opts.MaxAttempts {
		logger.Error().Str("error", msg).Dur("elapsed", elapsed).Msg("Worker: task failed for good")
		metrics.CollectionTaskAttemptsTotal.WithLabelValues(task.Plugin, storage.TaskFailed).Inc()
		return tasks.FinishCollectionTask(wctx, task.ID, storage.TaskFailed, msg, out.Items, out.Facts)
	}
	notBefore := time.Now().Add(opts.Backoff * time.Duration(task.Attempts))
	logger.Warn().Str("error", msg).Dur("elapsed", elapsed).Time("retry_at", notBefore).Msg("Worker: attempt failed; the task is retried")
	return release(wctx, tasks, task, msg, notBefore, out, true)
}

func release(ctx context.Context, tasks Tasks, task storage.CollectionTask, msg string, notBefore time.Time, out Outcome, countAttempt bool) error {
	metrics.CollectionTaskAttemptsTotal.WithLabelValues(task.Plugin, "released").Inc()
	if err := tasks.ReleaseCollectionTask(ctx, task.ID, msg, notBefore, out.Items, out.Facts, countAttempt); err != nil {
		return fmt.Errorf("task %d: %w", task.ID, err)
	}
	return nil
}

// heartbeat renews the lease of the task every interval until ctx ends, and
// calls cancel when the task is no longer running.
func heartbeat(ctx context.Context, cancel context.CancelFunc, tasks Tasks, id int64, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			running, err := tasks.HeartbeatCollectionTask(ctx, id)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn().Err(err).Int64("task", id).Msg("Worker: heartbeat failed")
				}
				continue
			}
			if !running {
				cancel()
				return
			}
		}
	}
}
