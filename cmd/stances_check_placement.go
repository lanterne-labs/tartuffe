package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/pipeline"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	stancesCheckPlacementSample     int
	stancesCheckPlacementAllPersons bool
)

var stancesCheckPlacementCmd = &cobra.Command{
	Use:   "check-placement",
	Short: "Measure how often the topic assigner disagrees with where facts already sit",
	Long: `Draws a random sample of facts from multi-fact stances and re-assigns each one
among all of its person's stances — its own stance included, but with the fact
hidden from the evidence — then counts stays, moves (another stance) and splits
(a new topic). Read-only; every move and split is logged with both labels.

Defaults to the search queue persons; --all-persons samples across everyone.

Examples:
  ./dm stances check-placement --sample 200
  ./dm stances check-placement --sample 500 --all-persons`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runStancesCheckPlacement,
}

func init() {
	stancesCmd.AddCommand(stancesCheckPlacementCmd)
	stancesCheckPlacementCmd.Flags().IntVar(&stancesCheckPlacementSample, "sample", 200, "Number of facts to draw")
	stancesCheckPlacementCmd.Flags().BoolVar(&stancesCheckPlacementAllPersons, "all-persons", false, "Sample across all persons instead of the search queue")
}

func runStancesCheckPlacement(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	stanceManager, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
	}

	var personIDs []int64
	if !stancesCheckPlacementAllPersons {
		targets, err := pipeline.QueueTargets(ctx, storer)
		if err != nil {
			return fmt.Errorf("failed to get search queue: %w", err)
		}
		for _, t := range targets {
			personIDs = append(personIDs, t.PersonID)
		}
	}

	stats, err := core.CheckFactPlacements(ctx, storer, stanceManager, personIDs, stancesCheckPlacementSample)
	if err != nil {
		return err
	}
	printResults("PLACEMENT CHECK RESULTS", []resultRow{
		{Label: "Facts sampled", Value: stats.Sampled},
		{Label: "Stays (current stance confirmed)", Value: stats.Stays},
		{Label: "Moves (another stance)", Value: stats.Moves},
		{Label: "Splits (new topic)", Value: stats.Splits},
		{Label: "Failures", Value: stats.Failures},
	})
	return nil
}
