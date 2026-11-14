package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/complaints"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/digest"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/plugins/twitter"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/worker"
	"digital-mirror/backend/xbot"

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

	// XBOT_ENABLED=true starts the X bot beside the tasks (backend/xbot): a
	// draft before each slot on Telegram, a tweet once approved.
	if on, _ := strconv.ParseBool(os.Getenv("XBOT_ENABLED")); on {
		if err := startXBot(ctx, storer); err != nil {
			log.Error().Err(err).Msg("Worker: the X bot did not start")
		}
	}

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
	vote, err := botVote(polls.NewPGStore(storer.Pool()))
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

// startXBot runs the X bot until ctx ends. The blackouts of the poll events
// (the eve and the day of each round) are its electoral silence.
func startXBot(ctx context.Context, storer *storage.PostgresStorer) error {
	tg, err := xbot.NewTelegramFromEnv()
	if err != nil {
		return err
	}
	if tg == nil {
		return errors.New("no Telegram bot (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID): nobody to approve the tweets")
	}
	writer, err := xbot.NewLLMWriterFromEnv()
	if err != nil {
		return err
	}
	events := polls.NewPGStore(storer.Pool())
	bot := &xbot.Bot{Store: storer, Writer: writer, Telegram: tg,
		Silence: func(ctx context.Context, t time.Time) (bool, error) {
			list, err := events.ListEvents(ctx)
			if err != nil {
				return false, err
			}
			for i := range list {
				if polls.InBlackout(&list[i], t) {
					return true, nil
				}
			}
			return false, nil
		}}
	if bot.Vote, err = botVote(events); err != nil {
		return err
	}
	if bot.Vote == nil {
		log.Warn().Msg("Worker: no XBOT_VOTE_POLLS: the X bot tweets nothing about the vote")
	}
	if fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv(); err != nil {
		log.Warn().Err(err).Msg("Worker: no tweet search: the X bot proposes no reply under the candidates' tweets")
	} else {
		bot.FindTweets = func(ctx context.Context, query string, max int) ([]xbot.FoundTweet, error) {
			tweets, err := fetcher.FetchTweets(ctx, query, twitter.FetchOptions{MaxTweets: max})
			if err != nil {
				return nil, err
			}
			out := make([]xbot.FoundTweet, 0, len(tweets))
			for _, t := range tweets {
				if t.IsRetweet || t.IsReply {
					continue
				}
				out = append(out, xbot.FoundTweet{ID: t.ID, Text: t.Text, Author: t.AuthorHandle, URL: t.URL, Views: t.Views, Likes: t.Likes, At: t.CreatedAt})
			}
			return out, nil
		}
	}
	if p := xbot.NewPosterFromEnv(storer); p != nil {
		bot.Poster = p
	} else {
		log.Warn().Msg("Worker: no X keys (X_API_KEY, X_API_SECRET, X_ACCESS_TOKEN, X_ACCESS_SECRET, or X_CLIENT_ID, X_CLIENT_SECRET, X_REFRESH_TOKEN): the X bot drafts and asks, but posts nothing")
	}
	if err := bot.Config(); err != nil {
		return err
	}
	log.Info().Int("slots", len(bot.Slots)).Bool("link", bot.Link).Msg("Worker: X bot started")
	go bot.Run(ctx)
	return nil
}

// botVote gives the X bot the vote it tweets about: the first featured event
// open at t that XBOT_VOTE_POLLS names ("slug:id,…", the isoloir poll of each
// event, as the frontend's VITE_VOTE_POLLS), with the ballots its latest
// tally counted on the mirror (XBOT_VOTE_MIRROR, default the site's). Nil
// without XBOT_VOTE_POLLS.
func botVote(events *polls.PGStore) (func(ctx context.Context, t time.Time) (*xbot.VoteState, error), error) {
	ids := map[string]int{}
	for _, pair := range strings.Split(os.Getenv("XBOT_VOTE_POLLS"), ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		slug, id, ok := strings.Cut(pair, ":")
		n, err := strconv.Atoi(id)
		if !ok || err != nil {
			return nil, fmt.Errorf("XBOT_VOTE_POLLS %q: want slug:id", pair)
		}
		ids[slug] = n
	}
	if len(ids) == 0 {
		return nil, nil
	}
	mirror := os.Getenv("XBOT_VOTE_MIRROR")
	if mirror == "" {
		mirror = "https://vote.tartuffe.me/vote-data"
	}
	return func(ctx context.Context, t time.Time) (*xbot.VoteState, error) {
		list, err := events.ListEvents(ctx)
		if err != nil {
			return nil, err
		}
		for i := range list {
			e := &list[i]
			id, ok := ids[e.Slug]
			if !ok || !e.Featured || !polls.IsOpen(e, t) {
				continue
			}
			counted, err := xbot.CountedBallots(ctx, mirror, id)
			if err != nil {
				return nil, err
			}
			return &xbot.VoteState{Slug: e.Slug, MaxChoices: e.MaxChoices, MinVoters: e.MinVoters, Counted: counted}, nil
		}
		return nil, nil
	}, nil
}
