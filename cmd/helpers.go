package cmd

import (
	"context"
	"fmt"
	"os"

	"digital-mirror/backend/migrate"

	"github.com/rs/zerolog/log"
)

func ensureMigrations(ctx context.Context) error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}
	migrationsDir := os.Getenv("MIGRATIONS_DIR") // optional override, defaults to "supabase/migrations"
	log.Debug().Msg("Running database migrations check")
	return migrate.Run(ctx, dbURL, migrationsDir)
}
