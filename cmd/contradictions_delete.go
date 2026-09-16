package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	deletePersonFlag string
	deleteAllFlag    bool
)

var contradictionsDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete contradictions for a person or all persons.",
	Long: `Deletes contradictions from the database and recalculates honesty scores.
Use --person flag to delete contradictions for a specific person.
Use --all flag to delete all contradictions in the database.`,
	RunE: runContradictionDelete,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsDeleteCmd)
	contradictionsDeleteCmd.Flags().StringVarP(&deletePersonFlag, "person", "p", "", "Person name to delete contradictions for")
	contradictionsDeleteCmd.Flags().BoolVar(&deleteAllFlag, "all", false, "Delete all contradictions for all persons")
}

func runContradictionDelete(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if deletePersonFlag == "" && !deleteAllFlag {
		return fmt.Errorf("must specify either --person or --all flag")
	}

	if deletePersonFlag != "" && deleteAllFlag {
		return fmt.Errorf("cannot use both --person and --all flags")
	}

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	// Initialize storage
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	if deletePersonFlag != "" {
		// Delete contradictions for a specific person
		person, err := core.ResolvePersonID(ctx, storer, deletePersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}

		deletedCount, err := storer.DeleteContradictionsForPerson(ctx, person.ID)
		if err != nil {
			return fmt.Errorf("failed to delete contradictions: %w", err)
		}

		fmt.Printf("Deleted %d contradictions for %s (ID: %d)\n", deletedCount, person.Name, person.ID)
		fmt.Printf("Honesty score reset to 1.0\n")
	} else {
		// Delete all contradictions
		persons, err := storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("failed to get persons: %w", err)
		}

		totalDeleted := 0
		for _, person := range persons {
			deletedCount, err := storer.DeleteContradictionsForPerson(ctx, person.ID)
			if err != nil {
				log.Error().Err(err).Str("person", person.Name).Msg("Failed to delete contradictions")
				continue
			}
			if deletedCount > 0 {
				fmt.Printf("Deleted %d contradictions for %s\n", deletedCount, person.Name)
				totalDeleted += deletedCount
			}
		}

		fmt.Printf("\nTotal: deleted %d contradictions for %d persons\n", totalDeleted, len(persons))
	}

	return nil
}
