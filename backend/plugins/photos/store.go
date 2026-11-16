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

// SetPersonImageURL sets the image_url for a person. The small copy of the
// photo before is dropped with it: SetPersonPreviewURL records the new one.
func (s *Store) SetPersonImageURL(ctx context.Context, personID int64, imageURL string) error {
	_, err := s.pool.Exec(ctx, "UPDATE persons SET image_url = $1, image_preview_url = NULL WHERE id = $2", imageURL, personID)
	if err != nil {
		return fmt.Errorf("failed to set image URL for person %d: %w", personID, err)
	}
	return nil
}

// SetPersonPreviewURL records the address of the small copy of the person's
// photo (MakePreview), the image of their link previews.
func (s *Store) SetPersonPreviewURL(ctx context.Context, personID int64, previewURL string) error {
	_, err := s.pool.Exec(ctx, "UPDATE persons SET image_preview_url = $1 WHERE id = $2", previewURL, personID)
	if err != nil {
		return fmt.Errorf("failed to set the preview URL for person %d: %w", personID, err)
	}
	return nil
}

// PhotosWithoutPreview returns the persons who have a photo and no small
// copy of it.
func (s *Store) PhotosWithoutPreview(ctx context.Context) ([]PhotoWithoutPreview, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+storage.PersonSummaryColumns+`, p.image_url
		FROM persons p
		WHERE p.image_url <> '' AND p.image_preview_url IS NULL
		ORDER BY p.id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query the photos without preview: %w", err)
	}
	defer rows.Close()
	var out []PhotoWithoutPreview
	for rows.Next() {
		var t PhotoWithoutPreview
		if err := storage.ScanPersonSummary(rows, &t.Person, &t.ImageURL); err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetPersonsWithoutImage returns all persons that don't have an image_url set.
func (s *Store) GetPersonsWithoutImage(ctx context.Context) ([]storage.PersonSummary, error) {
	query := `
		SELECT ` + storage.PersonSummaryColumns + `
		FROM persons p
		WHERE p.image_url IS NULL
		ORDER BY p.name ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons without image: %w", err)
	}
	defer rows.Close()

	var persons []storage.PersonSummary
	for rows.Next() {
		var person storage.PersonSummary
		if err := storage.ScanPersonSummary(rows, &person); err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}
