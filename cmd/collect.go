package cmd

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/worker"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "Collection tasks: the facts of a person over a period, run by the worker",
	Long: `A collection task asks one plugin for the facts of one person over one
period. The worker (dm worker run) claims the tasks one at a time and runs
them as soon as it is free. A period a plugin has fetched is never fetched
again.`,
}

var collectAddCmd = &cobra.Command{
	Use:   "add [person-name]",
	Short: "Add the tasks of a person over a period, one per plugin",
	Long: `Create one task per plugin for the person over [--from, --to). --to
defaults to today. The plugins default to every plugin that reads its source
by period (` + strings.Join(worker.PeriodPlugins(), ", ") + `).`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runCollectAdd,
}

var collectListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List the tasks, newest first",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runCollectList,
}

var collectCancelCmd = &cobra.Command{
	Use:         "cancel [task-id...]",
	Short:       "Cancel pending or running tasks",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.MinimumNArgs(1),
	RunE:        runCollectCancel,
}

func init() {
	rootCmd.AddCommand(collectCmd)
	collectCmd.AddCommand(collectAddCmd, collectListCmd, collectCancelCmd)
	collectAddCmd.Flags().String("from", "", "first day of the period, YYYY-MM-DD (required)")
	collectAddCmd.Flags().String("to", "", "day after the last day of the period, YYYY-MM-DD (default: today)")
	collectAddCmd.Flags().StringSlice("plugins", nil, "plugins to ask (default: every plugin that reads by period)")
	collectAddCmd.Flags().Int("priority", 0, "tasks of a higher priority run first")
	collectAddCmd.Flags().Bool("not-candidate", false, "add the tasks although the person is not a candidate (the facts are stored without a stance)")
	collectAddCmd.Flags().String("operation", "", "name of the batch the tasks belong to, followed as one whole in Grafana (Operations), e.g. \"backfill 2y\"")
	_ = collectAddCmd.MarkFlagRequired("from")
	collectListCmd.Flags().String("status", "", "only this status (pending, running, done, failed, cancelled)")
	collectListCmd.Flags().Int("limit", 50, "maximum number of tasks")
}

func runCollectAdd(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	fromStr, _ := cmd.Flags().GetString("from")
	toStr, _ := cmd.Flags().GetString("to")
	names, _ := cmd.Flags().GetStringSlice("plugins")
	priority, _ := cmd.Flags().GetInt("priority")
	operation, _ := cmd.Flags().GetString("operation")

	since, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	until := worker.Day(time.Now())
	if toStr != "" {
		if until, err = time.Parse("2006-01-02", toStr); err != nil {
			return fmt.Errorf("--to: %w", err)
		}
	}
	if !since.Before(until) {
		return fmt.Errorf("--from %s must be before --to %s", since.Format("2006-01-02"), until.Format("2006-01-02"))
	}
	if len(names) == 0 {
		names = worker.PeriodPlugins()
	}
	known := map[string]bool{}
	for _, n := range worker.PeriodPlugins() {
		known[n] = true
	}
	for _, n := range names {
		if !known[n] {
			return fmt.Errorf("plugin %q does not read its source by period (known: %s)", n, strings.Join(worker.PeriodPlugins(), ", "))
		}
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	person, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil || !ok {
		return err
	}
	if err := requireCandidate(ctx, storer, person, cmd); err != nil {
		return err
	}

	tasks := make([]storage.NewCollectionTask, 0, len(names))
	for _, n := range names {
		tasks = append(tasks, storage.NewCollectionTask{PersonID: person.ID, Plugin: n, Since: since, Until: until, Priority: priority, RequestedBy: requester(), Operation: strings.TrimSpace(operation)})
	}
	added, err := storer.AddCollectionTasks(ctx, tasks)
	if err != nil {
		return err
	}
	days := int(until.Sub(since).Hours() / 24)
	windows := (days + worker.WindowDays - 1) / worker.WindowDays
	fmt.Printf("%d task(s) added for %s (ID %d), %s to %s: %d days, about %d search windows per plugin (%s)\n",
		added, person.Name, person.ID, since.Format("2006-01-02"), until.Format("2006-01-02"), days, windows, strings.Join(names, ", "))
	if added < len(tasks) {
		fmt.Printf("%d task(s) already open for this period; not added again\n", len(tasks)-added)
	}
	return nil
}

// requester names who asked for a task: the OS user, for the task list.
func requester() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func runCollectList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	status, _ := cmd.Flags().GetString("status")
	limit, _ := cmd.Flags().GetInt("limit")
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	tasks, err := storer.ListCollectionTasks(ctx, status, limit)
	if err != nil {
		return err
	}
	counts, err := storer.CountCollectionTasks(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-6s %-9s %-8s %-28s %-10s %-10s %4s %6s %6s  %s\n", "ID", "STATUS", "PLUGIN", "PERSON", "FROM", "TO", "TRY", "ITEMS", "FACTS", "NOTE")
	for _, t := range tasks {
		errText := t.LastError
		if len(errText) > 60 {
			errText = errText[:60] + "..."
		}
		fmt.Printf("%-6d %-9s %-8s %-28.28s %-10s %-10s %4d %6d %6d  %s\n", t.ID, t.Status, t.Plugin, t.PersonName,
			t.Since.Format("2006-01-02"), t.Until.Format("2006-01-02"), t.Attempts, t.Items, t.Facts, errText)
	}
	var parts []string
	for _, s := range []string{storage.TaskPending, storage.TaskRunning, storage.TaskDone, storage.TaskFailed, storage.TaskCancelled} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Printf("\n%s\n", strings.Join(parts, ", "))
	return nil
}

func runCollectCancel(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return fmt.Errorf("task id %q: %w", a, err)
		}
		ids = append(ids, id)
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	n, err := storer.CancelCollectionTasks(ctx, ids)
	if err != nil {
		return err
	}
	fmt.Printf("%d task(s) cancelled\n", n)
	return nil
}
