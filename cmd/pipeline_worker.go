package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// bookkeepingTimeout bounds the post-run queue writes (finish/release), which
// must complete even when the run context is already canceled by a shutdown.
const bookkeepingTimeout = 30 * time.Second

var pipelineWorkerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run queued pipelines, one at a time, until stopped",
	Long: `Long-lived worker for the pipeline job queue.

Polls pipeline_jobs, claims the highest-priority queued job and runs it to
completion before looking for the next one — so a nightly run and an ad-hoc
backfill never compete for the LLM or the search quota. An empty queue is idle;
enqueue with ` + "`dm pipeline enqueue`" + `.

While a job runs the worker heartbeats it. A job whose worker died (no heartbeat
for --stale-after) goes back on the queue until its attempt budget is spent. On
SIGINT/SIGTERM the in-flight job is handed back to the queue without consuming
an attempt, so a redeploy resumes it instead of losing it.`,
	Annotations:   map[string]string{"needs_db": "true"},
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineWorker,
}

func init() {
	pipelineCmd.AddCommand(pipelineWorkerCmd)

	pipelineWorkerCmd.Flags().Duration("interval", 15*time.Second, "how often to poll for queued jobs")
	pipelineWorkerCmd.Flags().Duration("heartbeat", 30*time.Second, "how often a running job refreshes its liveness stamp")
	pipelineWorkerCmd.Flags().Duration("stale-after", 15*time.Minute, "requeue a running job whose worker stopped heartbeating for this long")
}

func runPipelineWorker(cmd *cobra.Command, args []string) error {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	interval, _ := cmd.Flags().GetDuration("interval")
	heartbeat, _ := cmd.Flags().GetDuration("heartbeat")
	staleAfter, _ := cmd.Flags().GetDuration("stale-after")
	if interval <= 0 || heartbeat <= 0 || staleAfter <= 0 {
		return fmt.Errorf("--interval, --heartbeat and --stale-after must all be positive")
	}
	if staleAfter <= heartbeat {
		return fmt.Errorf("--stale-after (%s) must be greater than --heartbeat (%s), otherwise a healthy job gets requeued under itself", staleAfter, heartbeat)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	notifier := alerting.NewTelegramNotifierFromEnv()
	workerID := pipelineWorkerID()

	log.Info().
		Str("worker", workerID).
		Dur("interval", interval).
		Dur("stale_after", staleAfter).
		Msg("Pipeline worker started")

	for {
		if ctx.Err() != nil {
			log.Info().Str("worker", workerID).Msg("Pipeline worker stopped")
			return nil
		}

		if requeued, failed, err := storer.ReapStalePipelineJobs(ctx, staleAfter); err != nil {
			log.Error().Err(err).Msg("Pipeline worker: reaping stale jobs failed")
		} else if requeued > 0 || failed > 0 {
			log.Warn().Int("requeued", requeued).Int("failed", failed).
				Msg("Pipeline worker: recovered jobs abandoned by a dead worker")
		}

		job, err := storer.ClaimNextPipelineJob(ctx, workerID)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			log.Error().Err(err).Msg("Pipeline worker: claim failed, retrying next tick")
			sleepCtx(ctx, interval)
			continue
		}
		if job == nil {
			sleepCtx(ctx, interval)
			continue
		}

		runClaimedJob(ctx, storer, notifier, workerID, job, heartbeat)
	}
}

// pipelineWorkerID identifies this process in pipeline_jobs.worker_id, so an
// abandoned job can be told apart from one a live worker still owns.
func pipelineWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}

// runClaimedJob executes one claimed job and closes it out. It never returns an
// error: a failed job is recorded as such and the worker moves on.
func runClaimedJob(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier,
	workerID string, job *storage.PipelineJob, heartbeat time.Duration) {

	log.Info().Int64("job", job.ID).Str("name", job.Name).Int("attempt", job.Attempts).
		Msg("Pipeline worker: job claimed")

	cfg, cfgErr := parsePipelineConfig(job.Config)
	if cfgErr != nil {
		// A malformed config never becomes valid by retrying — fail it outright.
		log.Error().Err(cfgErr).Int64("job", job.ID).Msg("Pipeline worker: unusable job config")
		finishJob(storer, job.ID, storage.PipelineJobError, nil, cfgErr.Error())
		return
	}

	// The heartbeat proves this worker is alive. If the stamp can no longer be
	// written the job is no longer ours (a reaper handed it to someone else), so
	// the run is canceled rather than duplicated.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		beat(runCtx, storer, job.ID, workerID, heartbeat, cancelRun)
	}()

	runID, runErr := runPipeline(runCtx, storer, notifier, job.Name, cfg)
	cancelRun()
	<-hbDone

	// A shutdown signal is not a job failure: give the job back so the next
	// instance picks it up, without spending an attempt.
	if ctx.Err() != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
		defer cancel()
		if err := storer.ReleasePipelineJob(releaseCtx, job.ID, workerID); err != nil {
			log.Error().Err(err).Int64("job", job.ID).Msg("Pipeline worker: could not requeue interrupted job")
		} else {
			log.Warn().Int64("job", job.ID).Msg("Pipeline worker: shutdown — job returned to the queue")
		}
		return
	}

	status, errMsg := storage.PipelineJobSuccess, ""
	if runErr != nil {
		status, errMsg = storage.PipelineJobError, runErr.Error()
	}
	finishJob(storer, job.ID, status, &runID, errMsg)
	log.Info().Int64("job", job.ID).Str("name", job.Name).Str("status", status).
		Msg("Pipeline worker: job finished")
}

// finishJob writes the terminal state on a detached context so the queue is
// updated even when the run context is already dead.
func finishJob(storer *storage.PostgresStorer, jobID int64, status string, runID *int64, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
	defer cancel()
	if err := storer.FinishPipelineJob(ctx, jobID, status, runID, errMsg); err != nil {
		log.Error().Err(err).Int64("job", jobID).Str("status", status).
			Msg("Pipeline worker: failed to record job outcome")
	}
}

// beat refreshes the job's liveness stamp until ctx ends. Only genuine loss of
// ownership aborts the run (via onLost) — a transient database error is logged
// and retried on the next tick, because killing a multi-hour run over one failed
// UPDATE would be far worse than the duplicate risk it guards against.
func beat(ctx context.Context, storer *storage.PostgresStorer, jobID int64, workerID string,
	every time.Duration, onLost context.CancelFunc) {

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := storer.HeartbeatPipelineJob(ctx, jobID, workerID)
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return
			case errors.Is(err, storage.ErrPipelineJobNotOwned):
				log.Error().Err(err).Int64("job", jobID).
					Msg("Pipeline worker: lost ownership of the running job, aborting the run")
				onLost()
				return
			default:
				log.Warn().Err(err).Int64("job", jobID).
					Msg("Pipeline worker: heartbeat failed, retrying next tick")
			}
		}
	}
}

// sleepCtx waits for d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
