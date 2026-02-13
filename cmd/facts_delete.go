package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// factsDeleteCmd represents the facts delete command
var factsDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete facts for a person or party",
	Long: `Delete all facts associated with a specific person or party.

Examples:
  # Delete all facts for a person
  ./dm facts delete --person "Marine Le Pen"

  # Delete all facts for a party
  ./dm facts delete --party "Rassemblement national"`,
	RunE: deleteFacts,
}

func init() {
	factsCmd.AddCommand(factsDeleteCmd)
	factsDeleteCmd.Flags().StringP("person", "p", "", "Person name to delete facts for")
	factsDeleteCmd.Flags().String("party", "", "Party name to delete facts for")
}

func deleteFacts(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	personName, _ := cmd.Flags().GetString("person")
	partyName, _ := cmd.Flags().GetString("party")

	if personName == "" && partyName == "" {
		return fmt.Errorf("either --person or --party is required")
	}
	if personName != "" && partyName != "" {
		return fmt.Errorf("cannot specify both --person and --party")
	}

	targetName := personName
	targetType := "person"
	if partyName != "" {
		targetName = partyName
		targetType = "party"
	}

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	// Normalize the name to match database storage
	normalizedName := util.NormalizeString(targetName)

	// Get the person ID
	personID, err := storer.GetPersonIDByName(ctx, normalizedName)
	if err != nil {
		return fmt.Errorf("failed to find %s %q: %w", targetType, targetName, err)
	}

	// Count facts before deletion
	factCount, err := storer.CountFactsForPerson(ctx, personID)
	if err != nil {
		log.Warn().Err(err).Msg("Could not count facts before deletion")
	}

	if factCount == 0 {
		fmt.Printf("No facts found for %s: %s\n", targetType, targetName)
		return nil
	}

	fmt.Printf("About to delete %d facts for %s: %s\n", factCount, targetType, targetName)
	fmt.Print("Continue? (y/n): ")

	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("Cancelled.")
		return nil
	}

	// Delete facts
	deleted, err := storer.DeleteFactsForPerson(ctx, personID)
	if err != nil {
		return fmt.Errorf("failed to delete facts: %w", err)
	}

	fmt.Printf("Deleted %d facts for %s: %s (plugin metadata cleared, plugins will re-process)\n", deleted, targetType, targetName)
	log.Info().
		Str("target", targetName).
		Str("type", targetType).
		Int64("deleted", deleted).
		Msg("Facts and plugin metadata deleted")

	return nil
}
