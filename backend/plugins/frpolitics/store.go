package frpolitics

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pressSourceTypes are the sources whose facts the débats backfill wants to
// meet: transcripts of persons who also speak to the press are where "said in
// the hemicycle vs said to the press" contradictions live.
var pressSourceTypes = []string{"news", "twitter"}

func init() {
	core.RequireQuoteForSourceType(debatsSourceType)
}

// Store is the plugin's own database access: the tables only frpolitics reads
// and writes (processed_laws, skipped_laws) and the person lookups keyed by
// Assemblée actor id. Transcript documents live in the shared document state
// machine (source_documents).
type Store struct{ pool *pgxpool.Pool }

// NewStore binds a plugin store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// IsLawFullyProcessed checks if a law has been completely processed (all facts stored).
func (s *Store) IsLawFullyProcessed(ctx context.Context, lawKey string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM processed_laws WHERE law_key = $1)`
	var exists bool
	err := s.pool.QueryRow(ctx, query, lawKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if law %s is processed: %w", lawKey, err)
	}
	return exists, nil
}

// MarkLawAsProcessed records that a law has been fully processed (all facts stored).
// This is the "commit" marker that prevents reprocessing the law on retry.
func (s *Store) MarkLawAsProcessed(ctx context.Context, lawKey string, legislature int, title string, factsCount int) error {
	query := `
		INSERT INTO processed_laws (law_key, legislature, title, facts_count)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (law_key) DO UPDATE
		SET processed_at = CURRENT_TIMESTAMP,
		    facts_count = EXCLUDED.facts_count
	`
	_, err := s.pool.Exec(ctx, query, lawKey, legislature, title, factsCount)
	if err != nil {
		return fmt.Errorf("failed to mark law %s as processed: %w", lawKey, err)
	}
	return nil
}

// DeletePartialLawData removes all facts, fact_sources, contradictions, and fact_mentions
// associated with a law_key. This is used for cleanup before reprocessing an incomplete law.
// IMPORTANT: Only deletes if the law is NOT marked as fully processed in processed_laws table.
// Returns the number of facts deleted.
func (s *Store) DeletePartialLawData(ctx context.Context, lawKey string) (int, error) {
	// Use a transaction to ensure atomic cleanup
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Safety check: Only delete if law is NOT in processed_laws table
	var isComplete bool
	checkQuery := `SELECT EXISTS(SELECT 1 FROM processed_laws WHERE law_key = $1)`
	if err := tx.QueryRow(ctx, checkQuery, lawKey).Scan(&isComplete); err != nil {
		return 0, fmt.Errorf("failed to check if law %s is complete: %w", lawKey, err)
	}
	if isComplete {
		// Law is marked as complete - do NOT delete its facts
		return 0, tx.Commit(ctx)
	}

	// Step 1: Find all fact IDs associated with this law_key
	factIDsQuery := `
		SELECT DISTINCT f.id
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id::bigint
		WHERE fs.metadata->>'law_key' = $1
	`
	rows, err := tx.Query(ctx, factIDsQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to find facts for law %s: %w", lawKey, err)
	}

	factIDs := make([]int64, 0)
	for rows.Next() {
		var factID int64
		if err := rows.Scan(&factID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("failed to scan fact ID: %w", err)
		}
		factIDs = append(factIDs, factID)
	}
	rows.Close()

	if len(factIDs) == 0 {
		// No facts to delete
		return 0, tx.Commit(ctx)
	}

	// Step 2: Delete contradictions referencing these facts
	deleteContradictionsQuery := `
		DELETE FROM contradictions
		WHERE fact_a_id = ANY($1) OR fact_b_id = ANY($1)
	`
	_, err = tx.Exec(ctx, deleteContradictionsQuery, factIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to delete contradictions for law %s: %w", lawKey, err)
	}

	// Step 3: Delete fact_mentions (via fact_source_id)
	deleteMentionsQuery := `
		DELETE FROM fact_mentions
		WHERE fact_source_id IN (
			SELECT id FROM fact_sources WHERE metadata->>'law_key' = $1
		)
	`
	_, err = tx.Exec(ctx, deleteMentionsQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to delete fact_mentions for law %s: %w", lawKey, err)
	}

	// Step 4: Delete fact_sources
	deleteSourcesQuery := `DELETE FROM fact_sources WHERE metadata->>'law_key' = $1`
	_, err = tx.Exec(ctx, deleteSourcesQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to delete fact_sources for law %s: %w", lawKey, err)
	}

	// Step 5: Delete facts (orphaned by removing fact_sources)
	deleteFactsQuery := `DELETE FROM facts WHERE id = ANY($1)`
	_, err = tx.Exec(ctx, deleteFactsQuery, factIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to delete facts for law %s: %w", lawKey, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return len(factIDs), nil
}

// StoreSkippedLaw stores information about a law that was skipped during processing
func (s *Store) StoreSkippedLaw(ctx context.Context, lawKey, title string, legislature int, voteDate *time.Time, skipReason, matchedPattern string, aiConfidence *float64) error {
	query := `
		INSERT INTO skipped_laws (law_key, title, legislature, vote_date, skip_reason, matched_pattern, ai_confidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (law_key) DO NOTHING`

	_, err := s.pool.Exec(ctx, query, lawKey, title, legislature, voteDate, skipReason, matchedPattern, aiConfidence)
	if err != nil {
		return fmt.Errorf("failed to store skipped law %s: %w", lawKey, err)
	}
	return nil
}

// GetPersonsByAssembleeID returns every person carrying an Assemblée actor id
// (attributes.assemblee_id, "PA..."), keyed by that id. The débats ingestion
// joins transcript paragraphs to persons on this id — no name matching. When
// two rows carry the same id (un-merged duplicates), the one with more facts
// wins, then the lowest id, so the choice is deterministic.
func (s *Store) GetPersonsByAssembleeID(ctx context.Context) (map[string]storage.PersonInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (attributes->>'assemblee_id')
		       id, name, COALESCE(entity_type, 'human'), attributes->>'assemblee_id'
		FROM persons
		WHERE attributes ? 'assemblee_id'
		ORDER BY attributes->>'assemblee_id', fact_count DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying persons by assemblee_id: %w", err)
	}
	defer rows.Close()
	out := map[string]storage.PersonInfo{}
	for rows.Next() {
		var p storage.PersonInfo
		var aid string
		if err := rows.Scan(&p.ID, &p.Name, &p.EntityType, &aid); err != nil {
			return nil, fmt.Errorf("scanning person by assemblee_id: %w", err)
		}
		if aid != "" {
			out[aid] = p
		}
	}
	return out, rows.Err()
}
