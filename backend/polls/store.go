package polls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one poll (the 2027 presidential pre-poll first): a voter ranks up
// to MaxChoices of its candidates.
type Event struct {
	ID            int64      `json:"id"`
	Slug          string     `json:"slug"`
	TitleFR       string     `json:"title_fr"`
	TitleEN       string     `json:"title_en"`
	DescriptionFR string     `json:"description_fr"`
	DescriptionEN string     `json:"description_en"`
	MaxChoices    int        `json:"max_choices"`
	MinVoters     int        `json:"min_voters"`
	OpensAt       time.Time  `json:"opens_at"`
	ClosesAt      *time.Time `json:"closes_at,omitempty"`
	Featured      bool       `json:"featured"`
	Blackouts     []Blackout `json:"blackouts"`
}

// Blackout is a period when the results of an event are not shown.
type Blackout struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// Card is what the polls show of a candidate: the people they are come from
// the mirror (People), the polls keep only their ids.
type Card struct {
	Name string
	// Slug names the person's page on the site (/person/<slug>).
	Slug               string
	ImageURL           *string
	HonestyScore       float64
	FactCount          int
	ContradictionCount int
}

// Store is what the polls keep of the events: the events, their blackouts
// and candidates. It owns the tables poll_events, poll_blackouts and
// poll_candidates.
type Store interface {
	ListEvents(ctx context.Context) ([]Event, error)
	GetEvent(ctx context.Context, slug string) (*Event, error)
	CandidateIDs(ctx context.Context, eventID int64) ([]int64, error)
}

// PGStore is the Postgres Store, and the public log.
type PGStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewPGStore keeps the polls in the pool's database.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, now: time.Now}
}

const eventColumns = `id, slug, title_fr, title_en, description_fr, description_en, max_choices, min_voters, opens_at, closes_at, featured`

func scanEvent(row pgx.Row) (*Event, error) {
	var e Event
	if err := row.Scan(&e.ID, &e.Slug, &e.TitleFR, &e.TitleEN, &e.DescriptionFR, &e.DescriptionEN,
		&e.MaxChoices, &e.MinVoters, &e.OpensAt, &e.ClosesAt, &e.Featured); err != nil {
		return nil, err
	}
	e.Blackouts = []Blackout{}
	return &e, nil
}

// ListEvents returns every event, the featured ones first, then the newest.
func (s *PGStore) ListEvents(ctx context.Context) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+eventColumns+` FROM poll_events ORDER BY featured DESC, opens_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list poll events: %w", err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan poll event: %w", err)
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list poll events: %w", err)
	}
	for i := range events {
		if events[i].Blackouts, err = s.blackouts(ctx, events[i].ID); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// GetEvent returns the event of that slug, or nil.
func (s *PGStore) GetEvent(ctx context.Context, slug string) (*Event, error) {
	e, err := scanEvent(s.pool.QueryRow(ctx, `SELECT `+eventColumns+` FROM poll_events WHERE slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get poll event %s: %w", slug, err)
	}
	if e.Blackouts, err = s.blackouts(ctx, e.ID); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *PGStore) blackouts(ctx context.Context, eventID int64) ([]Blackout, error) {
	rows, err := s.pool.Query(ctx, `SELECT starts_at, ends_at FROM poll_blackouts WHERE event_id = $1 ORDER BY starts_at`, eventID)
	if err != nil {
		return nil, fmt.Errorf("poll blackouts of event %d: %w", eventID, err)
	}
	defer rows.Close()
	out := []Blackout{}
	for rows.Next() {
		var b Blackout
		if err := rows.Scan(&b.StartsAt, &b.EndsAt); err != nil {
			return nil, fmt.Errorf("scan poll blackout: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveEvent creates the event of e.Slug or updates it, blackouts left as
// they are, and returns its id. A featured event unfeatures the others.
func (s *PGStore) SaveEvent(ctx context.Context, e Event) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("save poll event: %w", err)
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO poll_events (slug, title_fr, title_en, description_fr, description_en, max_choices, min_voters, opens_at, closes_at, featured)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (slug) DO UPDATE SET
			title_fr = EXCLUDED.title_fr, title_en = EXCLUDED.title_en,
			description_fr = EXCLUDED.description_fr, description_en = EXCLUDED.description_en,
			max_choices = EXCLUDED.max_choices, min_voters = EXCLUDED.min_voters,
			opens_at = EXCLUDED.opens_at, closes_at = EXCLUDED.closes_at, featured = EXCLUDED.featured
		RETURNING id`,
		e.Slug, e.TitleFR, e.TitleEN, e.DescriptionFR, e.DescriptionEN, e.MaxChoices, e.MinVoters, e.OpensAt, e.ClosesAt, e.Featured).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save poll event %s: %w", e.Slug, err)
	}
	if e.Featured {
		if _, err := tx.Exec(ctx, `UPDATE poll_events SET featured = false WHERE id <> $1 AND featured`, id); err != nil {
			return 0, fmt.Errorf("unfeature other poll events: %w", err)
		}
	}
	return id, tx.Commit(ctx)
}

// AddBlackout hides the results of the event over [from, to).
func (s *PGStore) AddBlackout(ctx context.Context, eventID int64, from, to time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO poll_blackouts (event_id, starts_at, ends_at) VALUES ($1, $2, $3)
		ON CONFLICT (event_id, starts_at) DO UPDATE SET ends_at = EXCLUDED.ends_at`, eventID, from, to)
	if err != nil {
		return fmt.Errorf("add poll blackout: %w", err)
	}
	return nil
}

// ClearBlackouts removes every blackout of the event.
func (s *PGStore) ClearBlackouts(ctx context.Context, eventID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM poll_blackouts WHERE event_id = $1`, eventID)
	if err != nil {
		return 0, fmt.Errorf("clear poll blackouts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AddCandidate makes the person a candidate of the event; false when they
// already were.
func (s *PGStore) AddCandidate(ctx context.Context, eventID, personID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO poll_candidates (event_id, person_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, eventID, personID)
	if err != nil {
		return false, fmt.Errorf("add poll candidate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RemoveCandidate removes the candidate and their place on every ballot;
// the ballots that ranked them below keep their order.
func (s *PGStore) RemoveCandidate(ctx context.Context, eventID, personID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM poll_candidates WHERE event_id = $1 AND person_id = $2`, eventID, personID)
	if err != nil {
		return false, fmt.Errorf("remove poll candidate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CandidateIDs returns the person ids of the event's candidates.
func (s *PGStore) CandidateIDs(ctx context.Context, eventID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT person_id FROM poll_candidates WHERE event_id = $1 ORDER BY person_id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("poll candidates of event %d: %w", eventID, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("poll candidates of event %d: %w", eventID, err)
	}
	return ids, nil
}

// pollUpdated logs a poll_updated event of the event (event_log, read by the
// SSE stream): the open result pages refetch.
func pollUpdated(ctx context.Context, tx pgx.Tx, eventID int64) error {
	payload, err := json.Marshal(map[string]any{})
	if err != nil {
		return fmt.Errorf("poll_updated payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_log (topic, entity_type, entity_id, payload) VALUES ('poll_updated', 'poll_event', $1, $2)`,
		eventID, payload); err != nil {
		return fmt.Errorf("log poll_updated: %w", err)
	}
	return nil
}

// SetClock makes the store read the time from now (tests).
func (s *PGStore) SetClock(now func() time.Time) { s.now = now }

// Pool is the store's pool (tests).
func (s *PGStore) Pool() *pgxpool.Pool { return s.pool }
