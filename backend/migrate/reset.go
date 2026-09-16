package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// resetTables are the data tables `dm db reset` truncates, in order. Every
// truncation cascades, so the order only affects log output.
var resetTables = []string{
	"contradictions",
	"fact_mentions",
	"fact_sources",
	"facts",
	"person_links",
	"skipped_laws",
	"persons",
}

// ResetTables truncates every data table (RESTART IDENTITY CASCADE) in one
// transaction: either all of them are emptied or none is.
func ResetTables(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction for DB reset: %w", err)
	}
	defer tx.Rollback(ctx)

	log.Warn().Msg("Starting database table truncation. This is a destructive operation!")
	for _, tableName := range resetTables {
		log.Info().Str("table", tableName).Msg("Truncating table...")
		// Table names come from the fixed list above, never from user input.
		query := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", tableName)
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("truncate table %s: %w", tableName, err)
		}
		log.Info().Str("table", tableName).Msg("Successfully truncated table.")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction for DB reset: %w", err)
	}
	return nil
}
