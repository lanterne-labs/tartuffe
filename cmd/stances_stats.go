package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var stancesStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show aggregate stance document statistics",
	Long: `Display aggregate statistics for stance documents across all persons.

Shows total stance count, total linked facts, and per-person breakdowns.

Examples:
  ./dm stances stats`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesStats,
}

func init() {
	stancesCmd.AddCommand(stancesStatsCmd)
}

func runStancesStats(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	// Get aggregate counts
	totalStances, totalFactsLinked, err := storer.GetStanceDocumentCounts(ctx)
	if err != nil {
		return fmt.Errorf("failed to get stance counts: %w", err)
	}

	fmt.Println("=== STANCE DOCUMENT STATISTICS ===")
	fmt.Printf("Total stance documents: %d\n", totalStances)
	fmt.Printf("Total facts linked:     %d\n", totalFactsLinked)

	if totalStances == 0 {
		fmt.Println("\nNo stance documents exist. Run 'stances backfill' to create them.")
		return nil
	}

	fmt.Printf("Avg facts per stance:   %.1f\n", float64(totalFactsLinked)/float64(totalStances))

	// Per-person breakdown
	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		return fmt.Errorf("failed to get persons: %w", err)
	}

	fmt.Println("\n--- Per-Person Breakdown ---")
	for _, p := range persons {
		stances, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to get stances")
			continue
		}
		if len(stances) == 0 {
			continue
		}

		totalFacts := 0
		for _, s := range stances {
			totalFacts += s.FactCount
		}
		fmt.Printf("  %-30s %3d stances, %4d facts linked\n", p.Name, len(stances), totalFacts)
	}

	return nil
}
