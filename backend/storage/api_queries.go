package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Ping checks database connectivity (used by the HTTP health endpoints).
func (s *PostgresStorer) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// MemberInfo represents a person who is a member/affiliate of another entity.
type MemberInfo struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	EntityType   string  `json:"entity_type"`
	HonestyScore float64 `json:"honesty_score"`
	ImageURL     *string `json:"image_url,omitempty"`
}

// GetPersonMembers returns the persons linked to an entity via person_links
// (e.g. the deputies of a party), ordered by name.
func (s *PostgresStorer) GetPersonMembers(ctx context.Context, personID int64) ([]MemberInfo, error) {
	query := `
		SELECT DISTINCT p.id, p.name, p.entity_type, p.honesty_score, p.image_url
		FROM persons p
		JOIN person_links pl ON p.id = pl.src_person_id
		WHERE pl.dst_person_id = $1
		ORDER BY p.name`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to get members: %w", err)
	}
	defer rows.Close()

	members := make([]MemberInfo, 0)
	for rows.Next() {
		var m MemberInfo
		if err := rows.Scan(&m.ID, &m.Name, &m.EntityType, &m.HonestyScore, &m.ImageURL); err != nil {
			return nil, fmt.Errorf("failed to scan member: %w", err)
		}
		members = append(members, m)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating members: %w", rows.Err())
	}
	return members, nil
}

// Event is one row of the event_log table, streamed to SSE clients.
type Event struct {
	ID         int64           `json:"id"`
	Topic      string          `json:"topic"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
}

// GetMaxEventID returns the newest event_log id, or 0 when the table is empty.
func (s *PostgresStorer) GetMaxEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, "SELECT COALESCE(MAX(id), 0) FROM event_log").Scan(&id); err != nil {
		return 0, fmt.Errorf("failed to query max event ID: %w", err)
	}
	return id, nil
}

// GetEventsAfter returns up to limit events with id > sinceID, oldest first.
func (s *PostgresStorer) GetEventsAfter(ctx context.Context, sinceID int64, limit int) ([]Event, error) {
	query := `
		SELECT id, topic, entity_type, entity_id, payload, created_at
		FROM event_log
		WHERE id > $1
		ORDER BY id ASC
		LIMIT $2`

	rows, err := s.pool.Query(ctx, query, sinceID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Topic, &event.EntityType, &event.EntityID, &event.Payload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating events: %w", rows.Err())
	}
	return events, nil
}
