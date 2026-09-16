package storage

import (
	"context"

	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// QueueEntry represents a person in the news queue.
type QueueEntry struct {
	ID         int64
	PersonID   int64
	Name       string
	EntityType string
	AddedAt    time.Time
}

// AddToSearchQueue adds a person to the search queue.
// Returns an error if the person doesn't exist or if the database operation fails.
// If the person is already in the queue, this is a no-op (returns nil).
func (s *PostgresStorer) AddToSearchQueue(ctx context.Context, personID int64) error {
	stmt := `
		INSERT INTO search_queue (person_id)
		VALUES ($1)
		ON CONFLICT (person_id) DO NOTHING`

	result, err := s.pool.Exec(ctx, stmt, personID)
	if err != nil {
		return fmt.Errorf("failed to add person %d to search queue: %w", personID, err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		log.Debug().Int64("person_id", personID).Msg("Person already in search queue")
	} else {
		log.Info().Int64("person_id", personID).Msg("Added person to search queue")
	}

	return nil
}

// RemoveFromSearchQueue removes a person from the search queue.
// Returns an error if the person is not in the queue.
func (s *PostgresStorer) RemoveFromSearchQueue(ctx context.Context, personID int64) error {
	stmt := `DELETE FROM search_queue WHERE person_id = $1`

	result, err := s.pool.Exec(ctx, stmt, personID)
	if err != nil {
		return fmt.Errorf("failed to remove person %d from search queue: %w", personID, err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("person %d not found in search queue", personID)
	}

	log.Info().Int64("person_id", personID).Msg("Removed person from search queue")
	return nil
}

// GetSearchQueue retrieves all persons in the search queue.
// Results are ordered by person_id ASC (oldest persons first).
func (s *PostgresStorer) GetSearchQueue(ctx context.Context) ([]QueueEntry, error) {
	query := `
		SELECT q.id, q.person_id, p.name, COALESCE(p.entity_type, 'human'), q.added_at
		FROM search_queue q
		JOIN persons p ON q.person_id = p.id
		ORDER BY q.person_id ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get search queue: %w", err)
	}
	defer rows.Close()

	queue := make([]QueueEntry, 0)
	for rows.Next() {
		var entry QueueEntry
		err := rows.Scan(&entry.ID, &entry.PersonID, &entry.Name, &entry.EntityType, &entry.AddedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan queue entry: %w", err)
		}
		queue = append(queue, entry)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating search queue: %w", rows.Err())
	}

	return queue, nil
}

// IsInSearchQueue checks if a person is in the search queue.
func (s *PostgresStorer) IsInSearchQueue(ctx context.Context, personID int64) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM search_queue WHERE person_id = $1)`

	var exists bool
	err := s.pool.QueryRow(ctx, query, personID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if person %d is in queue: %w", personID, err)
	}

	return exists, nil
}
