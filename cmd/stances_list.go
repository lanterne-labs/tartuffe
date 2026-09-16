package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var stancesListPerson string

var stancesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stance documents",
	Long: `List stance documents for a person or all persons.

Shows cluster label, fact count, and truncated summary for each stance.

Examples:
  # List stances for a specific person
  ./dm stances list --person "Marine Le Pen"

  # List stances for all persons
  ./dm stances list`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesList,
}

func init() {
	stancesCmd.AddCommand(stancesListCmd)
	stancesListCmd.Flags().StringVar(&stancesListPerson, "person", "", "Specific person name (optional, lists all if omitted)")
}

func runStancesList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	// Determine which persons to list
	var persons []storage.PersonInfo
	if stancesListPerson != "" {
		p, err := core.ResolvePersonID(ctx, storer, stancesListPerson, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		persons = []storage.PersonInfo{toPersonInfo(p)}
	} else {
		persons, err = storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("failed to get persons: %w", err)
		}
	}

	totalStances := 0

	for _, p := range persons {
		stances, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to get stances")
			continue
		}

		if len(stances) == 0 {
			continue
		}

		fmt.Printf("\n## %s (%d stances)\n", p.Name, len(stances))
		for _, s := range stances {
			summary := util.TruncateString(s.Summary, 100)
			fmt.Printf("  [%d] %s (%d facts) — %s\n", s.ID, util.TruncateString(s.ClusterLabel, 50), s.FactCount, summary)
		}
		totalStances += len(stances)
	}

	if totalStances == 0 {
		fmt.Println("No stance documents found.")
	} else {
		fmt.Printf("\nTotal: %d stance documents\n", totalStances)
	}

	return nil
}
