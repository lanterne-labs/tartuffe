package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"errors"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// personsAddCmd represents the persons add command
var personsAddCmd = &cobra.Command{
	Use:   "add [name]",
	Short: "Add a new person to the database.",
	Long:  `Adds a new person to be tracked by the fact extraction pipeline. The name will be normalized (lowercase, trimmed).`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return errors.New("requires exactly one argument: the person's name")
		}
		if args[0] == "" {
			return errors.New("person name cannot be empty")
		}
		return nil
	},
	Run: addPerson,
}

func init() {
	personsCmd.AddCommand(personsAddCmd)
}

func addPerson(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	personName := args[0]
	normalizedName := util.NormalizeString(personName)

	log.Info().Str("original_name", personName).Str("normalized_name", normalizedName).Msg("Attempting to add person")

	// Load environment variables just in case DB URL isn't globally set
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	// Initialize storer
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database storer")
		return // Redundant due to Fatal, but good practice
	}
	defer storer.Close()

	// Add the person
	id, inserted, err := storer.AddPerson(ctx, personName) 
	if err != nil {
		log.Error().Err(err).Str("name", normalizedName).Msg("Failed to add person")
		return
	}

	if inserted {
		log.Info().Int64("id", id).Str("name", normalizedName).Msg("Successfully added new person")
	} else {
		log.Info().Int64("id", id).Str("name", normalizedName).Msg("Person already exists")
	}
} 