package storage

import (
	"context"
	"fmt"
)

// PersonCard is what another part of the site (the polls) shows of a person.
type PersonCard struct {
	Name string
	// Slug names the person's page (/person/<slug>).
	Slug               string
	ImageURL           *string
	HonestyScore       float64
	FactCount          int
	ContradictionCount int
}

// GetPersonCards returns the cards of the persons, by id; an unknown id has
// no card.
func (s *PostgresStorer) GetPersonCards(ctx context.Context, ids []int64) (map[int64]PersonCard, error) {
	out := make(map[int64]PersonCard, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, p.slug, p.image_url, p.honesty_score, p.fact_count,
			(SELECT COUNT(*) FROM contradictions c WHERE c.person_id = p.id)
		FROM persons p WHERE p.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("person cards: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c PersonCard
		if err := rows.Scan(&id, &c.Name, &c.Slug, &c.ImageURL, &c.HonestyScore, &c.FactCount, &c.ContradictionCount); err != nil {
			return nil, fmt.Errorf("scan person card: %w", err)
		}
		out[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("person cards: %w", err)
	}
	return out, nil
}
