package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	stancesReassignDryRun     bool
	stancesReassignUntil      string
	stancesReassignStartAfter int64
	stancesReassignMaxPersons int
)

var stancesReassignSingletonsCmd = &cobra.Command{
	Use:   "reassign-singletons",
	Short: "Re-run topic assignment for every one-fact stance against the person's other stances",
	Long: `For each person with a one-fact stance and at least one other stance, asks the
topic assigner (shortlist + label pass + decision, as at ingest) where the fact
belongs among the OTHER stances. When it picks one, the singleton is merged
into it: the fact moves, contradiction bundles are repointed, the fact is
re-analyzed against the new stance's facts and the stance's label and summary
are refreshed. When it still says "new topic", the singleton stays.

Search queue members are processed first, then by person id. The sweep stops
starting persons at --until and logs the last person id; resume with
--start-after.

Examples:
  ./dm stances reassign-singletons --dry-run --max-persons 20
  ./dm stances reassign-singletons --until 2026-09-21T00:45:00Z
  ./dm stances reassign-singletons --start-after 1234`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runStancesReassignSingletons,
}

func init() {
	stancesCmd.AddCommand(stancesReassignSingletonsCmd)
	stancesReassignSingletonsCmd.Flags().BoolVar(&stancesReassignDryRun, "dry-run", false, "Decide without merging anything")
	stancesReassignSingletonsCmd.Flags().StringVar(&stancesReassignUntil, "until", "", "Stop starting persons after this RFC 3339 time")
	stancesReassignSingletonsCmd.Flags().Int64Var(&stancesReassignStartAfter, "start-after", 0, "Skip persons up to and including this person id (resume a cut run)")
	stancesReassignSingletonsCmd.Flags().IntVar(&stancesReassignMaxPersons, "max-persons", 0, "Process at most this many persons (0 = all)")
}

func runStancesReassignSingletons(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	opts := core.ReassignOptions{DryRun: stancesReassignDryRun, StartAfter: stancesReassignStartAfter, MaxPersons: stancesReassignMaxPersons}
	if stancesReassignUntil != "" {
		until, err := time.Parse(time.RFC3339, stancesReassignUntil)
		if err != nil {
			return fmt.Errorf("invalid --until (expected RFC 3339, e.g. 2026-09-21T00:45:00Z): %w", err)
		}
		opts.Deadline = until
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	stanceManager, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
	}

	stats, err := core.RunSingletonReassign(ctx, storer, stanceManager, opts)
	if err != nil {
		return err
	}
	printResults("SINGLETON RE-ASSIGNMENT RESULTS", []resultRow{
		{Label: "Persons processed", Value: stats.Persons},
		{Label: "Singletons judged", Value: stats.Singletons},
		{Label: "Joined an existing stance", Value: stats.Joined},
		{Label: "Kept as own topic", Value: stats.Kept},
		{Label: "Failures (stance kept)", Value: stats.Failures},
		{Label: "Contradictions found on join", Value: stats.Contradictions},
	})
	if stancesReassignDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}
