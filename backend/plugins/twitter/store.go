package twitter

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// The plugin's per-person tables, maintained by the generic person operations
// through the registry: handles follow a merged person (unique on
// (person_id, handle), so colliding rows are dropped) and the fetch log is
// cleared when a person's facts are deleted so tweets get re-processed.
func init() {
	core.RequireQuoteForSourceType("twitter")
	storage.RegisterPersonTable(storage.PersonTable{
		Name:       "twitter_handles",
		RepointSQL: `UPDATE twitter_handles th SET person_id=$2 WHERE th.person_id=$1 AND NOT EXISTS (SELECT 1 FROM twitter_handles x WHERE x.person_id=$2 AND x.handle=th.handle)`,
		CleanupSQL: `DELETE FROM twitter_handles WHERE person_id=$1`,
		ResetSQL:   `DELETE FROM twitter_fetch_log WHERE handle_id IN (SELECT id FROM twitter_handles WHERE person_id = $1)`,
	})
}

// Store is the plugin's own persistence: handles linked to persons, the
// discovery search mark and the per-handle fetch log.
type Store struct{ pool *pgxpool.Pool }

// NewStore binds a Store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// Handle represents a Twitter handle linked to a person.
type Handle struct {
	ID            int64
	PersonID      int64
	PersonName    string
	Handle        string
	LastFetchedAt *time.Time
	Active        bool
}

// AddHandle links a Twitter handle to a person.
// If the handle already exists for this person, this is a no-op.
func (s *Store) AddHandle(ctx context.Context, personID int64, handle string) error {
	stmt := `
		INSERT INTO twitter_handles (person_id, handle)
		VALUES ($1, $2)
		ON CONFLICT (person_id, handle) DO NOTHING`

	result, err := s.pool.Exec(ctx, stmt, personID, handle)
	if err != nil {
		return fmt.Errorf("failed to add twitter handle @%s for person %d: %w", handle, personID, err)
	}

	if result.RowsAffected() == 0 {
		log.Debug().Int64("person_id", personID).Str("handle", handle).Msg("Twitter handle already exists")
	} else {
		log.Info().Int64("person_id", personID).Str("handle", handle).Msg("Added twitter handle")
	}

	return nil
}

// RemoveHandle removes a Twitter handle from a person.
func (s *Store) RemoveHandle(ctx context.Context, personID int64, handle string) error {
	stmt := `DELETE FROM twitter_handles WHERE person_id = $1 AND handle = $2`

	result, err := s.pool.Exec(ctx, stmt, personID, handle)
	if err != nil {
		return fmt.Errorf("failed to remove twitter handle @%s for person %d: %w", handle, personID, err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("twitter handle @%s not found for person %d", handle, personID)
	}

	log.Info().Int64("person_id", personID).Str("handle", handle).Msg("Removed twitter handle")
	return nil
}

// GetActiveHandles returns all active Twitter handles with their person names.
func (s *Store) GetActiveHandles(ctx context.Context) ([]Handle, error) {
	query := `
		SELECT th.id, th.person_id, p.name, th.handle, th.last_fetched_at, th.active
		FROM twitter_handles th
		JOIN persons p ON th.person_id = p.id
		WHERE th.active = true
		ORDER BY th.person_id ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active twitter handles: %w", err)
	}
	defer rows.Close()

	var handles []Handle
	for rows.Next() {
		var h Handle
		if err := rows.Scan(&h.ID, &h.PersonID, &h.PersonName, &h.Handle, &h.LastFetchedAt, &h.Active); err != nil {
			return nil, fmt.Errorf("failed to scan twitter handle: %w", err)
		}
		handles = append(handles, h)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating twitter handles: %w", rows.Err())
	}

	return handles, nil
}

// GetHandlesByPersonID returns all Twitter handles for a specific person.
func (s *Store) GetHandlesByPersonID(ctx context.Context, personID int64) ([]Handle, error) {
	query := `
		SELECT th.id, th.person_id, p.name, th.handle, th.last_fetched_at, th.active
		FROM twitter_handles th
		JOIN persons p ON th.person_id = p.id
		WHERE th.person_id = $1
		ORDER BY th.handle ASC`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to get twitter handles for person %d: %w", personID, err)
	}
	defer rows.Close()

	var handles []Handle
	for rows.Next() {
		var h Handle
		if err := rows.Scan(&h.ID, &h.PersonID, &h.PersonName, &h.Handle, &h.LastFetchedAt, &h.Active); err != nil {
			return nil, fmt.Errorf("failed to scan twitter handle: %w", err)
		}
		handles = append(handles, h)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating twitter handles: %w", rows.Err())
	}

	return handles, nil
}

// MarkSearched sets twitter_searched_at to now for a person,
// so that discovery won't re-search them until the cooldown expires.
func (s *Store) MarkSearched(ctx context.Context, personID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE persons SET twitter_searched_at = NOW() WHERE id = $1`, personID)
	if err != nil {
		return fmt.Errorf("failed to mark twitter searched for person %d: %w", personID, err)
	}
	return nil
}

// GetSearchedAt returns the last time Twitter discovery was attempted for a person.
func (s *Store) GetSearchedAt(ctx context.Context, personID int64) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT twitter_searched_at FROM persons WHERE id = $1`, personID).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("failed to get twitter_searched_at for person %d: %w", personID, err)
	}
	return t, nil
}

// UpdateHandleLastFetched updates the last_fetched_at timestamp for a handle.
func (s *Store) UpdateHandleLastFetched(ctx context.Context, handleID int64, t time.Time) error {
	stmt := `UPDATE twitter_handles SET last_fetched_at = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, stmt, handleID, t)
	if err != nil {
		return fmt.Errorf("failed to update last_fetched_at for handle %d: %w", handleID, err)
	}
	return nil
}

// LogFetch records that a date range was fetched for a handle.
func (s *Store) LogFetch(ctx context.Context, handleID int64, dateFrom, dateTo time.Time, tweetsFetched int) error {
	stmt := `
		INSERT INTO twitter_fetch_log (handle_id, date_from, date_to, tweets_fetched)
		VALUES ($1, $2, $3, $4)`

	_, err := s.pool.Exec(ctx, stmt, handleID, dateFrom, dateTo, tweetsFetched)
	if err != nil {
		return fmt.Errorf("failed to log twitter fetch for handle %d: %w", handleID, err)
	}
	return nil
}

// IsRangeFetched checks if a date range has already been fetched for a handle.
// Returns true if an existing log entry fully covers the requested range.
func (s *Store) IsRangeFetched(ctx context.Context, handleID int64, dateFrom, dateTo time.Time) (bool, error) {
	query := `SELECT EXISTS(
		SELECT 1 FROM twitter_fetch_log
		WHERE handle_id = $1 AND date_from <= $2 AND date_to >= $3
	)`

	var exists bool
	err := s.pool.QueryRow(ctx, query, handleID, dateFrom, dateTo).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check twitter fetch log for handle %d: %w", handleID, err)
	}

	return exists, nil
}
