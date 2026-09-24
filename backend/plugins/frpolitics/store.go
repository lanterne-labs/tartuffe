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

// StoredLawFactTexts returns the texts of the facts already stored for a law.
// A law whose store failed part-way resumes from them: every ballot is its
// own fact with a text unique to its voter, and each insert is atomic, so
// the ballots already stored are skipped rather than deleted and redone.
func (s *Store) StoredLawFactTexts(ctx context.Context, lawKey string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT f.fact_text
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE fs.source_type = 'assemblee_law_page' AND fs.metadata->>'law_key' = $1`, lawKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load stored facts for law %s: %w", lawKey, err)
	}
	defer rows.Close()
	stored := make(map[string]bool)
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, fmt.Errorf("failed to scan stored fact for law %s: %w", lawKey, err)
		}
		stored[text] = true
	}
	return stored, rows.Err()
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
