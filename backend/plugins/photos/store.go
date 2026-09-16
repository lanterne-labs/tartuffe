package photos

import (
	"context"
	"fmt"

	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the plugin-owned persistence: the persons.image_url column.
type Store struct{ pool *pgxpool.Pool }

// NewStore binds the store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// SetPersonImageURL sets the image_url for a person.
func (s *Store) SetPersonImageURL(ctx context.Context, personID int64, imageURL string) error {
	_, err := s.pool.Exec(ctx, "UPDATE persons SET image_url = $1 WHERE id = $2", imageURL, personID)
	if err != nil {
		return fmt.Errorf("failed to set image URL for person %d: %w", personID, err)
	}
	return nil
}

// GetPersonsWithoutImage returns all persons that don't have an image_url set.
func (s *Store) GetPersonsWithoutImage(ctx context.Context) ([]storage.PersonSummary, error) {
	query := `
		SELECT id, name, entity_type, honesty_score, own_score, members_score, image_url
		FROM persons
		WHERE image_url IS NULL
		ORDER BY name ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons without image: %w", err)
	}
	defer rows.Close()

	var persons []storage.PersonSummary
	for rows.Next() {
		var person storage.PersonSummary
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.OwnScore, &person.MembersScore, &person.ImageURL)
		if err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}
