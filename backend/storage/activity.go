package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ActivityEvent struct {
	ID         int64           `json:"id"`
	Topic      string          `json:"topic"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
	Person     *ActivityPerson `json:"person,omitempty"`
}

type ActivityPerson struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
}

type ActivityFeed struct {
	Items []ActivityEvent `json:"items"`
	Total int             `json:"total"`
}

// GetActivityFeed returns recent contradiction_added and person_updated events
// enriched with the associated person's name, image, and honesty score.
// Events are ordered newest-first.
func (s *PostgresStorer) GetActivityFeed(ctx context.Context, limit int, offset int) (*ActivityFeed, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_log
		WHERE topic IN ('contradiction_added', 'person_updated')
	`).Scan(&total); err != nil {
		return nil, fmt.Errorf("activity count: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT el.id, el.topic, el.entity_type, el.entity_id, el.payload, el.created_at,
		       p.id, p.name, p.image_url, p.honesty_score
		FROM event_log el
		LEFT JOIN persons p ON el.entity_type = 'person' AND el.entity_id = p.id
		WHERE el.topic IN ('contradiction_added', 'person_updated')
		ORDER BY el.created_at DESC, el.id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("activity query: %w", err)
	}
	defer rows.Close()

	items := make([]ActivityEvent, 0, limit)
	for rows.Next() {
		var e ActivityEvent
		var personID *int64
		var personName *string
		var personImageURL *string
		var personScore *float64

		if err := rows.Scan(
			&e.ID, &e.Topic, &e.EntityType, &e.EntityID, &e.Payload, &e.CreatedAt,
			&personID, &personName, &personImageURL, &personScore,
		); err != nil {
			return nil, fmt.Errorf("activity scan: %w", err)
		}

		if personID != nil && personName != nil && personScore != nil {
			e.Person = &ActivityPerson{
				ID:           *personID,
				Name:         *personName,
				ImageURL:     personImageURL,
				HonestyScore: *personScore,
			}
		}

		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("activity rows: %w", err)
	}

	return &ActivityFeed{Items: items, Total: total}, nil
}
