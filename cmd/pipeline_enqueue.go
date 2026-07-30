package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// dateLayout is the only accepted date format for pipeline job flags.
const dateLayout = "2006-01-02"

var pipelineEnqueueCmd = &cobra.Command{
	Use:   "enqueue [name]",
	Short: "Queue a pipeline run for the worker to pick up",
	Long: `Queues a pipeline run. The worker (` + "`dm pipeline worker`" + `) runs queued jobs one
at a time, in priority then FIFO order.

With no flags this queues the nightly pipeline (every step, the search queue, no
date restriction) under the name "daily" — that is what the cron service runs.
Flags narrow the run: specific targets, a publication date window, a subset of
steps.

The name defaults to a slug of the configuration, and only one job per name can
be pending at a time: re-running the same enqueue while the job is still queued
or running is a no-op, so a cron tick can never stack two nightly runs.

Examples:
  # what the cron service runs
  dm pipeline enqueue daily

  # everything we can find about Macron published on 2017-05-07
  dm pipeline enqueue --target "Emmanuel Macron" --date 2017-05-07

  # a month of Le Pen news, one search window per week, news + contradictions only
  dm pipeline enqueue --target "Marine Le Pen" --from 2024-06-01 --to 2024-06-30 \
      --window-days 7 --steps news,contradictions --max-parse 20`,
	Annotations:   map[string]string{"needs_db": "true"},
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineEnqueue,
}

var pipelineJobsCmd = &cobra.Command{
	Use:           "jobs",
	Short:         "List pipeline jobs (pending first, then most recent)",
	Annotations:   map[string]string{"needs_db": "true"},
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineJobs,
}

var pipelineCancelCmd = &cobra.Command{
	Use:           "cancel [job-id]",
	Short:         "Cancel a queued pipeline job (a running one must be stopped at the worker)",
	Annotations:   map[string]string{"needs_db": "true"},
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineCancel,
}

func init() {
	pipelineCmd.AddCommand(pipelineEnqueueCmd)
	pipelineCmd.AddCommand(pipelineJobsCmd)
	pipelineCmd.AddCommand(pipelineCancelCmd)

	pipelineEnqueueCmd.Flags().StringSlice("target", nil, "person/party to process (repeatable; default: the search queue)")
	pipelineEnqueueCmd.Flags().String("date", "", "restrict search to a single publication date (YYYY-MM-DD)")
	pipelineEnqueueCmd.Flags().String("from", "", "restrict search to articles published on or after this date (YYYY-MM-DD)")
	pipelineEnqueueCmd.Flags().String("to", "", "restrict search to articles published on or before this date (YYYY-MM-DD)")
	pipelineEnqueueCmd.Flags().StringSlice("steps", nil, "only run these steps (default: all of "+strings.Join(allPipelineSteps, ", ")+")")
	pipelineEnqueueCmd.Flags().StringSlice("skip-steps", nil, "run everything except these steps")
	pipelineEnqueueCmd.Flags().Int("max-parse", 0, "max articles parsed per target (0 = default)")
	pipelineEnqueueCmd.Flags().Int("window-days", 0, "split the date range into windows of N days (0 = one window)")
	pipelineEnqueueCmd.Flags().String("location", "", "restrict news search to this country (default: France)")
	pipelineEnqueueCmd.Flags().Int("priority", 100, "lower runs first")

	pipelineJobsCmd.Flags().String("status", "", "filter by status (queued, running, success, error, canceled)")
	pipelineJobsCmd.Flags().Int("limit", 30, "maximum jobs to list")
}

func runPipelineEnqueue(cmd *cobra.Command, args []string) error {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	ctx := context.Background()

	cfg, err := pipelineConfigFromFlags(cmd)
	if err != nil {
		return err
	}
	priority, _ := cmd.Flags().GetInt("priority")

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Resolve target names now, so a typo fails at the terminal instead of on the
	// worker three hours from now. Store the canonical names.
	canonical, err := canonicalizeTargets(ctx, storer, cfg.Targets)
	if err != nil {
		return err
	}
	cfg.Targets = canonical

	if err := cfg.Validate(); err != nil {
		return err
	}

	name := cfg.DefaultName()
	if len(args) == 1 && args[0] != "" {
		name = args[0]
	}

	payload, err := cfg.MarshalConfig()
	if err != nil {
		return fmt.Errorf("serializing pipeline config: %w", err)
	}

	job, created, err := storer.EnqueuePipelineJob(ctx, name, payload, priority)
	if err != nil {
		return err
	}
	if !created {
		fmt.Printf("job %q is already %s (id %d, queued %s) — nothing enqueued\n",
			job.Name, job.Status, job.ID, job.QueuedAt.Format(time.RFC3339))
		return nil
	}

	fmt.Printf("queued job %d %q\n", job.ID, job.Name)
	fmt.Printf("  steps:   %s\n", strings.Join(cfg.enabledSteps(), ", "))
	fmt.Printf("  targets: %s\n", targetsLabel(cfg))
	fmt.Printf("  window:  %s\n", cfg.windowLabel())
	return nil
}

// pipelineConfigFromFlags builds the job config from the enqueue flags.
func pipelineConfigFromFlags(cmd *cobra.Command) (PipelineConfig, error) {
	var cfg PipelineConfig

	targets, _ := cmd.Flags().GetStringSlice("target")
	dateStr, _ := cmd.Flags().GetString("date")
	fromStr, _ := cmd.Flags().GetString("from")
	toStr, _ := cmd.Flags().GetString("to")
	steps, _ := cmd.Flags().GetStringSlice("steps")
	skipSteps, _ := cmd.Flags().GetStringSlice("skip-steps")
	maxParse, _ := cmd.Flags().GetInt("max-parse")
	windowDays, _ := cmd.Flags().GetInt("window-days")
	location, _ := cmd.Flags().GetString("location")

	if dateStr != "" && (fromStr != "" || toStr != "") {
		return cfg, fmt.Errorf("--date cannot be combined with --from/--to")
	}
	if len(steps) > 0 && len(skipSteps) > 0 {
		return cfg, fmt.Errorf("--steps and --skip-steps are mutually exclusive")
	}

	if dateStr != "" {
		fromStr, toStr = dateStr, dateStr
	}
	from, err := parseWindowDate(fromStr, false)
	if err != nil {
		return cfg, fmt.Errorf("--from: %w", err)
	}
	// The end of the window is inclusive of the whole day: --to 2024-06-30 must
	// cover articles published at 18:00 that day.
	to, err := parseWindowDate(toStr, true)
	if err != nil {
		return cfg, fmt.Errorf("--to: %w", err)
	}

	resolvedSteps, err := resolveSteps(steps, skipSteps)
	if err != nil {
		return cfg, err
	}

	cfg = PipelineConfig{
		Targets:    targets,
		DateFrom:   from,
		DateTo:     to,
		Steps:      resolvedSteps,
		MaxParse:   maxParse,
		WindowDays: windowDays,
		Location:   location,
	}
	return cfg, cfg.Validate()
}

// resolveSteps turns --steps / --skip-steps into the explicit enabled list that
// gets stored on the job. Neither flag given = nil = every step.
func resolveSteps(steps, skipSteps []string) ([]string, error) {
	for _, s := range append(append([]string{}, steps...), skipSteps...) {
		if !knownPipelineStep(s) {
			return nil, fmt.Errorf("unknown pipeline step %q (known: %s)", s, strings.Join(allPipelineSteps, ", "))
		}
	}
	if len(steps) > 0 {
		return steps, nil
	}
	if len(skipSteps) == 0 {
		return nil, nil
	}
	skip := make(map[string]bool, len(skipSteps))
	for _, s := range skipSteps {
		skip[s] = true
	}
	var out []string
	for _, s := range allPipelineSteps {
		if !skip[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--skip-steps skips every step, nothing left to run")
	}
	return out, nil
}

// parseWindowDate parses a YYYY-MM-DD flag in UTC. endOfDay pushes the timestamp
// to 23:59:59 so a single-day window is inclusive.
func parseWindowDate(s string, endOfDay bool) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("expected YYYY-MM-DD, got %q", s)
	}
	if endOfDay {
		t = t.Add(24*time.Hour - time.Second)
	}
	return &t, nil
}

// canonicalizeTargets resolves each name to an existing entity and returns the
// stored spelling. An ambiguous or unknown name is a hard error listing the
// closest candidates — a job must never silently run on the wrong person.
func canonicalizeTargets(ctx context.Context, storer *storage.PostgresStorer, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		res := core.ResolveTargetName(ctx, storer, name)
		if res.Error != nil {
			return nil, fmt.Errorf("resolving target %q: %w", name, res.Error)
		}
		if res.IsExactMatch {
			out = append(out, res.ExactMatch.Name)
			continue
		}
		if len(res.FuzzyMatches) == 0 {
			return nil, fmt.Errorf("no entity matches target %q (add it first with `dm persons add`)", name)
		}
		var candidates []string
		for _, m := range res.FuzzyMatches {
			candidates = append(candidates, m.Target.Name)
		}
		return nil, fmt.Errorf("target %q is ambiguous — did you mean one of: %s",
			name, strings.Join(candidates, ", "))
	}
	return out, nil
}

func targetsLabel(cfg PipelineConfig) string {
	if !cfg.scoped() {
		return "search queue"
	}
	return strings.Join(cfg.Targets, ", ")
}

func runPipelineJobs(cmd *cobra.Command, args []string) error {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	ctx := context.Background()

	status, _ := cmd.Flags().GetString("status")
	limit, _ := cmd.Flags().GetInt("limit")

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	jobs, err := storer.ListPipelineJobs(ctx, status, limit)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Println("no pipeline jobs")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tQUEUED\tSTARTED\tDURATION\tTRY\tDETAIL")
	for _, j := range jobs {
		cfg, cfgErr := parsePipelineConfig(j.Config)
		detail := "-"
		if cfgErr != nil {
			detail = "invalid config: " + cfgErr.Error()
		} else if cfg.scoped() || cfg.hasDateWindow() || len(cfg.Steps) > 0 {
			detail = fmt.Sprintf("targets=%s window=%s steps=%d",
				targetsLabel(cfg), cfg.windowLabel(), len(cfg.enabledSteps()))
		}
		if j.Error != nil && *j.Error != "" {
			detail = firstLine(*j.Error)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			j.ID, j.Name, j.Status,
			j.QueuedAt.Local().Format("01-02 15:04"),
			formatOptionalTime(j.StartedAt),
			jobDuration(j),
			j.Attempts, j.MaxAttempts,
			detail)
	}
	return w.Flush()
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("01-02 15:04")
}

// jobDuration is the elapsed time of a finished job, or the running time so far
// of one in flight.
func jobDuration(j storage.PipelineJob) string {
	if j.StartedAt == nil {
		return "-"
	}
	end := time.Now()
	if j.FinishedAt != nil {
		end = *j.FinishedAt
	}
	return end.Sub(*j.StartedAt).Round(time.Second).String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func runPipelineCancel(cmd *cobra.Command, args []string) error {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	ctx := context.Background()

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid job id %q: %w", args[0], err)
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	if err := storer.CancelPipelineJob(ctx, id); err != nil {
		return err
	}
	fmt.Printf("canceled job %d\n", id)
	return nil
}
