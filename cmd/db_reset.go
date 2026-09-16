package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/migrate"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// dbResetCmd represents the db reset command
var dbResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Resets (truncates) all relevant data tables in the database.",
	Long: `TRUNCATES the following tables: persons, facts, fact_sources, fact_mentions, person_fact_sources, and contradictions.
This is a destructive operation and will remove all data from these tables.`,
	Run: executeDbReset,
}

func init() {
	dbCmd.AddCommand(dbResetCmd)
}

func executeDbReset(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	log.Info().Msg("Attempting to reset database tables...")

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading, relying on environment variables.")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer for DB reset")
	}
	defer storer.Close()

	if err := migrate.ResetTables(ctx, storer.Pool()); err != nil {
		log.Fatal().Err(err).Msg("Failed to reset database tables")
	}

	log.Info().Msg("Database reset completed successfully.")
	fmt.Println("All specified database tables have been reset.")
}
