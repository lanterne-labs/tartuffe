package migrate

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
)

const defaultMigrationsDir = "supabase/migrations"

func Run(ctx context.Context, dbURL string, migrationsDir string) error {
	if migrationsDir == "" {
		migrationsDir = defaultMigrationsDir
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return fmt.Errorf("failed to open database for migrations: %w", err)
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	// Handle pre-goose databases: if the DB has tables but no goose tracking,
	// stamp all existing migrations as applied so goose only runs new ones.
	if err := stampExistingIfNeeded(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("failed to stamp existing migrations: %w", err)
	}

	log.Info().Str("dir", migrationsDir).Msg("Running database migrations...")
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	log.Info().Int64("version", version).Msg("Database migrations complete")
	return nil
}

// stampExistingIfNeeded detects a database that was previously managed by Supabase's
// migration runner (tables exist, but goose has no version tracking). In that case,
// it stamps all migration versions into goose's tracking table so goose.Up only
// runs genuinely new migrations.
func stampExistingIfNeeded(ctx context.Context, db *sql.DB, migrationsDir string) error {
	// Check if goose is already tracking this database
	version, err := goose.GetDBVersionContext(ctx, db)
	if err == nil && version > 0 {
		return nil // Already tracking
	}

	// Check if the database has existing tables (applied by Supabase)
	var factsExists bool
	err = db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'facts')").Scan(&factsExists)
	if err != nil {
		return fmt.Errorf("failed to check for existing tables: %w", err)
	}

	if !factsExists {
		return nil // Fresh database, let goose handle everything from scratch
	}

	// Pre-goose database detected: stamp all known migrations as applied
	migrations, err := goose.CollectMigrations(migrationsDir, 0, goose.MaxVersion)
	if err != nil {
		return fmt.Errorf("failed to collect migrations: %w", err)
	}

	log.Info().Int("count", len(migrations)).Msg("Detected pre-goose database, stamping existing migrations")

	// Ensure goose version table exists
	if _, err := goose.EnsureDBVersion(db); err != nil {
		return fmt.Errorf("failed to create goose version table: %w", err)
	}

	// Stamp each migration version as applied (without running it)
	for _, m := range migrations {
		// Check if this migration's target table already exists.
		// We stamp it only if its changes are already in the database.
		// Use a heuristic: if the migration version predates the newest table,
		// it's already applied. For simplicity, check if the specific table
		// from the LATEST migration exists — if not, don't stamp that one.
		if !migrationAlreadyApplied(ctx, db, m.Version) {
			log.Info().Int64("version", m.Version).Msg("Migration not yet applied, will run normally")
			continue
		}

		_, err := db.ExecContext(ctx,
			"INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true) ON CONFLICT DO NOTHING",
			m.Version)
		if err != nil {
			return fmt.Errorf("failed to stamp migration %d: %w", m.Version, err)
		}
		log.Debug().Int64("version", m.Version).Msg("Stamped migration as applied")
	}

	return nil
}

// migrationAlreadyApplied checks if a migration's changes are already in the database
// by looking for key tables created by specific migrations.
func migrationAlreadyApplied(ctx context.Context, db *sql.DB, version int64) bool {
	// Map migration versions to a table they create/modify.
	// Only need to check migrations that CREATE tables — ALTER migrations
	// are safe to re-run if the table exists.
	tableChecks := map[int64]string{
		1:              "facts",
		2:              "contradictions",
		20260109121000: "event_log",
		20260112000000: "news_search_log",
		20260114000002: "processed_laws",
		20260115000000: "search_queue", // originally news_queue, renamed by 20260207
		20260201000000: "rss_feeds",
		20260206000000: "twitter_handles",
		20260207000000: "search_queue",
		20260211000000: "job_metrics",
		20260310000000: "stance_documents",
		20260312000000: "llm_batches",
	}

	tableName, hasCheck := tableChecks[version]
	if !hasCheck {
		// No table to check — assume applied if facts table exists (base schema is there)
		return true
	}

	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
		tableName).Scan(&exists)
	if err != nil {
		return false // Can't check, don't stamp — let goose try to run it
	}

	return exists
}
