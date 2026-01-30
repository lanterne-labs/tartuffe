package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// personsListCmd represents the persons list command
var personsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all persons currently tracked in the database.",
	Long:  `Retrieves and prints a list of all unique person names stored in the persons table.`,
	Args:  cobra.NoArgs, // No arguments expected for list command
	Run:   listPersons,
}

func init() {
	personsCmd.AddCommand(personsListCmd)
}

func listPersons(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	log.Info().Msg("Attempting to list persons...")

	// Load environment variables if needed
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	// Initialize storer - No longer needs embedder
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	// Get all persons
	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to retrieve persons from database")
		return
	}

	if len(persons) == 0 {
		fmt.Println("No persons found in the database.")
		log.Info().Msg("No persons found in the database.")
	} else {
		fmt.Printf("Found %d persons:\n", len(persons))
		for _, p := range persons {
			fmt.Printf("- %s (%s)\n", p.Name, p.EntityType)
		}
		log.Info().Int("count", len(persons)).Msg("Successfully listed persons")
	}
}
