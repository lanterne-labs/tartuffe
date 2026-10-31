package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/worker"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "The worker that runs the collection tasks",
}

var workerRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Claim and run the collection tasks until stopped",
	Long: `Run as a long-lived service. The worker claims the pending tasks one at a
time, by priority then age, and runs the ingest of the task's plugin for its
person and period. When no task is pending it does the housekeeping: the
daily feed (frpolitics, the backfills, one task per watched person for the
previous day) and the maintenance pass (stance refresh and merge,
contradiction detection, regroup, audit, translations). It serves Prometheus
metrics on METRICS_PORT (default 9090). On SIGTERM the task it is on goes back
to pending.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runWorker,
}

func init() {
	rootCmd.AddCommand(workerCmd)
	workerCmd.AddCommand(workerRunCmd)
	workerRunCmd.Flags().Duration("poll", worker.DefaultOptions.Poll, "pause when no task is pending")
	workerRunCmd.Flags().Duration("lease", worker.DefaultOptions.Lease, "a running task without a heartbeat for this long is claimed again")
}

func runWorker(cmd *cobra.Command, args []string) error {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	go func() {
		port := metricsPort()
		log.Info().Str("port", port).Msg("Starting internal metrics server")
		if err := metrics.ListenAndServe(port, metrics.NewServeMux()); err != nil {
			log.Error().Err(err).Msg("Metrics server failed; the worker goes on without its metrics")
		}
	}()

	opts := worker.DefaultOptions
	opts.Poll, _ = cmd.Flags().GetDuration("poll")
	opts.Lease, _ = cmd.Flags().GetDuration("lease")
	if err := opts.Validate(); err != nil {
		return err
	}
	if notifier := alerting.NewTelegramNotifierFromEnv(); notifier != nil {
		opts.Notify = notifier.Send
		// The first credit error of each LLM provider sends one message.
		llm.OnCreditError = worker.CreditAlert(notifier.Send)
	}
	// The judgement counters reach the database as the work goes, not only
	// when the process ends.
	metrics.StartDecisionFlusher(ctx, storer, time.Minute)
	// With the home GPU unreachable, every call goes straight to the fallback
	// instead of waiting out the retry backoff per call.
	if !llm.ProbePrimary(ctx) {
		log.Warn().Msg("Worker: primary LLM endpoint unreachable; routing the calls to the fallback")
	}

	// WORKER_PAUSED=true stops all the work: no task is claimed, no feed and
	// no maintenance pass run; the process only serves its metrics until the
	// variable is removed (a variable change on the host restarts the
	// service). It is the switch to stop extracting while the data is
	// audited.
	if paused, _ := strconv.ParseBool(os.Getenv("WORKER_PAUSED")); paused {
		log.Warn().Msg("Worker: paused (WORKER_PAUSED=true); no task, feed or maintenance runs until the variable is removed")
		<-ctx.Done()
		log.Info().Msg("Worker stopped while paused")
		return nil
	}

	d := deps.New(storer)
	err = worker.Run(ctx, storer, worker.PluginRunner{Deps: d}, worker.NewHousekeeping(storer, d), opts)
	if errors.Is(err, context.Canceled) {
		log.Info().Msg("Worker stopped")
		return nil
	}
	return err
}
