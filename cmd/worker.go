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
	"digital-mirror/backend/complaints"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/digest"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
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
		// No message for an API key out of credit (the owner, 2026-11-14: the
		// key is spent every day, the endpoint halts and the logs say so).
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

	if judge, err := complaints.NewOpenAIJudgeFromEnv(); err != nil {
		log.Error().Err(err).Msg("Worker: no LLM client for the user reports; they stay pending")
	} else {
		go worker.RunReports(ctx, func(ctx context.Context) (int, error) {
			return complaints.JudgeReports(ctx, storer, judge, 10)
		}, worker.ReportsEvery)
	}

	startDigest(ctx, storer)

	d := deps.New(storer)
	err = worker.Run(ctx, storer, worker.PluginRunner{Deps: d}, worker.NewHousekeeping(storer, d), opts)
	if errors.Is(err, context.Canceled) {
		log.Info().Msg("Worker stopped")
		return nil
	}
	return err
}

// startDigest sends the daily digest on Telegram (backend/digest): the new
// facts and contradictions, the ballots of the vote, and whether something
// needs a person. DIGEST_AT is its time of day in Paris (default "08:00";
// "none": no digest).
func startDigest(ctx context.Context, storer *storage.PostgresStorer) {
	at := os.Getenv("DIGEST_AT")
	if at == "" {
		at = "08:00"
	}
	if at == "none" {
		return
	}
	clock, err := time.Parse("15:04", at)
	if err != nil {
		log.Error().Err(err).Str("DIGEST_AT", at).Msg("Worker: no daily digest: want a time of day (08:00)")
		return
	}
	notifier := alerting.NewTelegramNotifierFromEnv()
	if notifier == nil {
		log.Warn().Msg("Worker: no Telegram bot (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID): no daily digest")
		return
	}
	zone, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		log.Error().Err(err).Msg("Worker: no daily digest: time zone")
		return
	}
	errs := &digest.ErrorCounter{}
	log.Logger = log.Logger.Hook(errs)
	d := &digest.Digest{Store: storage.NewDigestStore(storer), Send: notifier.Send, Hour: clock.Hour(), Min: clock.Minute(), Zone: zone,
		MaintenanceEvery: worker.MaintenanceEvery(), Errors: errs, Now: time.Now}
	// VOTE_POLLS names the isoloir poll of each event ("slug:id,…"), VOTE_MIRROR
	// the mirror its tallies are read from (default the site's).
	vote, err := polls.FeaturedVote(polls.NewPGStore(storer.Pool()), os.Getenv("VOTE_POLLS"), os.Getenv("VOTE_MIRROR"))
	if err != nil {
		log.Error().Err(err).Msg("Worker: the digest has no vote line")
	} else if vote != nil {
		d.Ballots = func(ctx context.Context) (int, bool, error) {
			v, err := vote(ctx, time.Now())
			if err != nil || v == nil {
				return 0, false, err
			}
			return v.Counted, true, nil
		}
	}
	go d.Run(ctx)
}
