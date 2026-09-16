package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/pipeline"
	"digital-mirror/backend/plugins"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// queueCmd represents the queue command group
var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Manage the search queue",
	Long:  `Add, remove, list, and process persons in the search queue. The search queue is shared across all plugins.`,
}

// queueAddCmd adds a person to the search queue
var queueAddCmd = &cobra.Command{
	Use:         "add [person-name]",
	Short:       "Add a person to the search queue",
	Long:        `Add a person to the search queue. The person must already exist in the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueAdd,
}

// queueRemoveCmd removes a person from the search queue
var queueRemoveCmd = &cobra.Command{
	Use:         "remove [person-name]",
	Short:       "Remove a person from the search queue",
	Long:        `Remove a person from the search queue by name.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueRemove,
}

// queueListCmd lists all persons in the search queue
var queueListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List all persons in the search queue",
	Long:        `List all persons currently in the search queue, ordered by person ID.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runQueueList,
}

// queueProcessCmd processes the search queue
var queueProcessCmd = &cobra.Command{
	Use:         "process",
	Short:       "Run the ingest phase of the selected plugins over the search queue",
	Long:        `Process persons in the search queue through the ingest phase of the selected plugins. Plugin-specific tuning lives on each plugin's own command.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runQueueProcess,
}

func init() {
	rootCmd.AddCommand(queueCmd)

	queueCmd.AddCommand(queueAddCmd)
	queueCmd.AddCommand(queueRemoveCmd)
	queueCmd.AddCommand(queueListCmd)
	queueCmd.AddCommand(queueProcessCmd)

	queueProcessCmd.Flags().Int("limit", 0, "maximum number of persons to process (0 = all)")
	queueProcessCmd.Flags().String("since", "", "only search content published after YYYY-MM-DD")
	queueProcessCmd.Flags().Int("days", 0, "only search content from last N days (alternative to --since)")
	queueProcessCmd.Flags().Int("window-days", 0, "break date range into windows of N days (requires --since or --days, 0=disabled)")
	queueProcessCmd.Flags().StringSlice("plugins", []string{"news", "twitter"}, "plugins to run (registered: "+strings.Join(pipeline.Names(), ", ")+")")
	queueProcessCmd.Flags().Bool("force", false, "bypass the plugins' already-processed checks")
	queueProcessCmd.Flags().Bool("dry-run", false, "fetch and extract without storing anything")
}

func runQueueAdd(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	person, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil || !ok {
		return err
	}
	if err := storer.AddToSearchQueue(ctx, person.ID); err != nil {
		return fmt.Errorf("failed to add to queue: %w", err)
	}
	if inQueue, _ := storer.IsInSearchQueue(ctx, person.ID); inQueue {
		fmt.Printf("Added %s (ID: %d) to search queue\n", person.Name, person.ID)
	}
	return nil
}

func runQueueRemove(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	person, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil || !ok {
		return err
	}
	if err := storer.RemoveFromSearchQueue(ctx, person.ID); err != nil {
		return err
	}
	fmt.Printf("Removed %s (ID: %d) from search queue\n", person.Name, person.ID)
	return nil
}

func runQueueList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}
	if len(queue) == 0 {
		fmt.Println("Search queue is empty")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERSON ID\tNAME\tTYPE\tADDED AT")
	for _, entry := range queue {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", entry.PersonID, entry.Name, entry.EntityType, entry.AddedAt.Format("2006-01-02 15:04"))
	}
	w.Flush()
	fmt.Printf("\nTotal: %d persons in queue\n", len(queue))
	return nil
}

func runQueueProcess(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	limit, _ := cmd.Flags().GetInt("limit")
	sinceStr, _ := cmd.Flags().GetString("since")
	days, _ := cmd.Flags().GetInt("days")
	windowDays, _ := cmd.Flags().GetInt("window-days")
	pluginNames, _ := cmd.Flags().GetStringSlice("plugins")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if sinceStr != "" && days > 0 {
		return fmt.Errorf("--since and --days are mutually exclusive, use only one")
	}
	var since *time.Time
	if sinceStr != "" {
		t, err := time.Parse("2006-01-02", sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since format (expected YYYY-MM-DD): %w", err)
		}
		since = &t
	} else if days > 0 {
		t := time.Now().AddDate(0, 0, -days)
		since = &t
		log.Info().Int("days", days).Time("since", t).Msg("Calculated since date from --days flag")
	}
	if windowDays > 0 && since == nil {
		return fmt.Errorf("--window-days requires --since or --days to be set")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	targets, err := pipeline.QueueTargets(ctx, storer)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}
	if len(targets) == 0 {
		log.Info().Msg("Search queue is empty")
		return nil
	}
	if limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	log.Info().Int("queue_size", len(targets)).Strs("plugins", pluginNames).Msg("Starting queue processing")

	opts := plugins.IngestOptions{Since: since, WindowDays: windowDays, Force: force, DryRun: dryRun}
	stats, stepErrs, err := pipeline.Ingest(ctx, deps.New(storer), targets, opts, pluginNames)
	if err != nil {
		return err
	}

	fmt.Printf("\nProcessing complete:\n")
	fmt.Printf("  Plugins: %s\n", strings.Join(pluginNames, ", "))
	fmt.Printf("  Targets: %d\n", stats.Targets)
	fmt.Printf("  Items: %d\n", stats.Items)
	fmt.Printf("  Facts: %d\n", stats.Facts)
	fmt.Printf("  Item errors: %d\n", stats.Errors)
	for name, perr := range stepErrs {
		fmt.Printf("  %s failed: %v\n", name, perr)
	}

	if !dryRun {
		recordJobMetrics(ctx, storer, jobMetrics("queue-process", "", map[string]float64{
			"targets": float64(stats.Targets),
			"facts":   float64(stats.Facts),
			"errors":  float64(stats.Errors + len(stepErrs)),
		}))
	}
	if len(stepErrs) > 0 {
		return fmt.Errorf("%d plugin(s) failed", len(stepErrs))
	}
	return nil
}
