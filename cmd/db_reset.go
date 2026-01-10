package cmd

import (
	"context"
	"digital-mirror/backend/storage"
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer for DB reset")
	}
	defer storer.Close()

	tablesToTruncate := []string{
		"contradictions",
		"fact_mentions",
		"fact_sources",
		"facts",
		"person_links",
		"skipped_laws",
		"persons",
	}

	dbPool := storer.Pool() // Get the underlying pool from the storer

	tx, err := dbPool.Begin(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to begin transaction for DB reset")
	}
	defer tx.Rollback(ctx) // Rollback if anything goes wrong

	log.Warn().Msg("Starting database table truncation. This is a destructive operation!")
	for _, tableName := range tablesToTruncate {
		log.Info().Str("table", tableName).Msg("Truncating table...")
		// Use Sprintf to avoid SQL injection, though table names here are from a controlled list.
		// For dynamic table names from user input, more robust sanitization or checks would be needed.
		query := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", tableName)
		_, err := tx.Exec(ctx, query)
		if err != nil {
			log.Error().Err(err).Str("table", tableName).Msg("Failed to truncate table")
			// No need to Fatal here, let the rollback handle it and report a general failure.
			fmt.Printf("Error truncating table %s. Aborting reset.\n", tableName)
			return // Exit the function, defer will rollback
		}
		log.Info().Str("table", tableName).Msg("Successfully truncated table.")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatal().Err(err).Msg("Failed to commit transaction for DB reset")
	}

	log.Info().Msg("Database reset completed successfully.")
	fmt.Println("All specified database tables have been reset.")
}
