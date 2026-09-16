package news

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// The plugin's per-person table, maintained by the generic person operations
// through the registry: the search log follows a merged person and is cleared
// when a person's facts are deleted so the person is searched again.
func init() {
	core.RequireQuoteForSourceType("news")
	storage.RegisterPersonTable(storage.PersonTable{
		Name:       "news_search_log",
		RepointSQL: `UPDATE news_search_log SET person_id=$2 WHERE person_id=$1`,
		ResetSQL:   `DELETE FROM news_search_log WHERE person_id = $1`,
	})
}

// Store is the plugin's own persistence: the per-person search log used for
// prioritization and the news provenance of existing facts.
type Store struct{ pool *pgxpool.Pool }

// NewStore binds a Store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// LogNewsSearch records a news search attempt for prioritization.
// This helps track which persons have news coverage and which don't.
func (s *Store) LogNewsSearch(ctx context.Context, personID int64, query string, resultCount int, factsExtracted int) error {
	insertQuery := `
		INSERT INTO news_search_log (person_id, query, result_count, facts_extracted)
		VALUES ($1, $2, $3, $4)`

	_, err := s.pool.Exec(ctx, insertQuery, personID, query, resultCount, factsExtracted)
	if err != nil {
		return fmt.Errorf("failed to log news search for person %d: %w", personID, err)
	}

	log.Debug().
		Int64("person_id", personID).
		Str("query", query).
		Int("result_count", resultCount).
		Int("facts_extracted", factsExtracted).
		Msg("Logged news search")

	return nil
}

// ShouldSearchNews determines if we should search news for a person based on history.
// Returns true if:
// - No previous search exists (first time)
// - Previous search found results (facts_extracted > 0)
// - No search in the last lookbackDays
func (s *Store) ShouldSearchNews(ctx context.Context, personID int64, lookbackDays int) (bool, error) {
	query := `
		SELECT
			COUNT(*) as search_count,
			COALESCE(MAX(facts_extracted), 0) as max_facts,
			COALESCE(MAX(searched_at), '1970-01-01'::timestamptz) as last_search
		FROM news_search_log
		WHERE person_id = $1
		AND searched_at > NOW() - $2::interval`

	var searchCount int
	var maxFacts int
	var lastSearch time.Time
	interval := fmt.Sprintf("%d days", lookbackDays)

	err := s.pool.QueryRow(ctx, query, personID, interval).Scan(&searchCount, &maxFacts, &lastSearch)
	if err != nil {
		return false, fmt.Errorf("failed to check news search history for person %d: %w", personID, err)
	}

	// No recent search: should search
	if searchCount == 0 {
		return true, nil
	}

	// Previous searches found results: should search again
	if maxFacts > 0 {
		return true, nil
	}

	// Previous searches found no results: skip
	return false, nil
}

// NewsURLsForPerson returns the distinct news source_urls that have at least one
// fact_mention for the given person_id. Used by the rebuild-from-urls path to
// know which URLs to re-extract for each tracked person.
func (s *Store) NewsURLsForPerson(ctx context.Context, personID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT fs.source_url
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1 AND fs.source_type = 'news'
		ORDER BY fs.source_url`, personID)
	if err != nil {
		return nil, fmt.Errorf("NewsURLsForPerson: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
