package polls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one poll (the 2027 presidential pre-poll first): the users rank
// up to MaxChoices of its candidates.
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
	Name               string
	ImageURL           *string
	HonestyScore       float64
	FactCount          int
	ContradictionCount int
}

// CandidateTally is a candidate of an event with how many ballots rank them
// first, and at all, and their card.
type CandidateTally struct {
	PersonID     int64
	FirstChoices int
	Chosen       int
	Card
}

// UserChoice is one vote of a user, with the candidate it goes to.
type UserChoice struct {
	EventID      int64   `json:"event_id"`
	Rank         int     `json:"rank"`
	PersonID     int64   `json:"person_id"`
	Name         string  `json:"name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	FactCount    int     `json:"fact_count"`
}

// ErrNotCandidate is returned when a ballot names a person who is not (or
// no longer) a candidate of the event.
var ErrNotCandidate = errors.New("not a candidate of the event")

// ErrNotVerified is returned when an account without a verified identity
// adds a vote.
var ErrNotVerified = errors.New("the account has no verified identity: verify it to vote")

// Store is what the polls keep: the events, their blackouts and candidates,
// the ballots and the consents. It owns the tables poll_events,
// poll_blackouts, poll_candidates, poll_choices and poll_consents. The
// candidates' cards are not its own: Tallies and UserChoices leave them
// empty.
type Store interface {
	ListEvents(ctx context.Context) ([]Event, error)
	GetEvent(ctx context.Context, slug string) (*Event, error)
	CandidateIDs(ctx context.Context, eventID int64) ([]int64, error)
	Tallies(ctx context.Context, eventID int64) ([]CandidateTally, int, error)
	Ballot(ctx context.Context, eventID, userID int64) ([]int64, error)
	Consented(ctx context.Context, userID int64) (bool, error)
	UpdateBallot(ctx context.Context, eventID, userID int64, consent bool, edit func(current []int64) ([]int64, error)) ([]int64, error)
	UserChoices(ctx context.Context, userID int64) ([]UserChoice, error)
}

// LockVerified locks the account in the transaction and tells whether it
// holds a verified identity (accounts.LockVerified): the polls never read
// the accounts' tables themselves.
type LockVerified func(ctx context.Context, tx pgx.Tx, userID int64) (bool, error)

// PGStore is the Postgres Store.
type PGStore struct {
	pool         *pgxpool.Pool
	lockVerified LockVerified
}

// NewPGStore keeps the polls in the pool's database.
func NewPGStore(pool *pgxpool.Pool, lockVerified LockVerified) *PGStore {
	return &PGStore{pool: pool, lockVerified: lockVerified}
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

// Tallies returns the candidates of the event with their counts (no card),
// and the number of ballots. A ballot's first choice is its lowest rank, so
// a ballot whose rank 1 candidate was removed counts for its next one.
func (s *PGStore) Tallies(ctx context.Context, eventID int64) ([]CandidateTally, int, error) {
	rows, err := s.pool.Query(ctx, `
		WITH firsts AS (
			SELECT DISTINCT ON (user_id) user_id, person_id
			FROM poll_choices WHERE event_id = $1
			ORDER BY user_id, rank
		)
		SELECT pc0.person_id,
			(SELECT COUNT(*) FROM firsts f WHERE f.person_id = pc0.person_id),
			(SELECT COUNT(*) FROM poll_choices pc WHERE pc.event_id = $1 AND pc.person_id = pc0.person_id)
		FROM poll_candidates pc0
		WHERE pc0.event_id = $1
		ORDER BY pc0.person_id`, eventID)
	if err != nil {
		return nil, 0, fmt.Errorf("poll tallies of event %d: %w", eventID, err)
	}
	defer rows.Close()
	out := []CandidateTally{}
	for rows.Next() {
		var c CandidateTally
		if err := rows.Scan(&c.PersonID, &c.FirstChoices, &c.Chosen); err != nil {
			return nil, 0, fmt.Errorf("scan poll tally: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("poll tallies of event %d: %w", eventID, err)
	}
	var voters int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT user_id) FROM poll_choices WHERE event_id = $1`, eventID).Scan(&voters); err != nil {
		return nil, 0, fmt.Errorf("poll voters of event %d: %w", eventID, err)
	}
	return out, voters, nil
}

// Ballot returns the person ids the user ranked, first choice first.
func (s *PGStore) Ballot(ctx context.Context, eventID, userID int64) ([]int64, error) {
	return ballot(ctx, s.pool, eventID, userID)
}

func ballot(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, eventID, userID int64) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT person_id FROM poll_choices WHERE event_id = $1 AND user_id = $2 ORDER BY rank`, eventID, userID)
	if err != nil {
		return nil, fmt.Errorf("poll ballot: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("poll ballot: %w", err)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

// Consented tells whether the user agreed that their ballots (political
// opinions, GDPR art. 9) are stored.
func (s *PGStore) Consented(ctx context.Context, userID int64) (bool, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM poll_consents WHERE user_id = $1)`, userID).Scan(&ok); err != nil {
		return false, fmt.Errorf("poll consent of user %d: %w", userID, err)
	}
	return ok, nil
}

// UpdateBallot changes the user's ballot (person ids, first choice first) in
// one transaction: edit gets the ballot as stored and returns the new one
// (empty withdraws it), or an error that cancels the change and is returned
// as is. The account is locked first (LockVerified), so two requests of one
// user (two tabs, a double click) apply one after the other, each on the
// other's result, and a document moving to another account meanwhile takes
// the right to vote with it: a ballot that gains a vote needs the identity
// (ErrNotVerified). A changed ballot records the user's consent when consent
// is set, and logs a poll_updated event so the open result pages refresh. It
// returns the ballot as stored.
func (s *PGStore) UpdateBallot(ctx context.Context, eventID, userID int64, consent bool, edit func(current []int64) ([]int64, error)) ([]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update poll ballot: %w", err)
	}
	defer tx.Rollback(ctx)
	verified, err := s.lockVerified(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	current, err := ballot(ctx, tx, eventID, userID)
	if err != nil {
		return nil, err
	}
	next, err := edit(current)
	if err != nil {
		return nil, err
	}
	if slices.Equal(current, next) {
		return current, nil
	}
	if !verified && slices.ContainsFunc(next, func(id int64) bool { return !slices.Contains(current, id) }) {
		return nil, ErrNotVerified
	}
	if consent {
		if _, err := tx.Exec(ctx, `INSERT INTO poll_consents (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
			return nil, fmt.Errorf("record consent: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM poll_choices WHERE event_id = $1 AND user_id = $2`, eventID, userID); err != nil {
		return nil, fmt.Errorf("clear poll ballot: %w", err)
	}
	for i, pid := range next {
		if _, err := tx.Exec(ctx, `INSERT INTO poll_choices (event_id, user_id, rank, person_id) VALUES ($1, $2, $3, $4)`,
			eventID, userID, i+1, pid); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return nil, fmt.Errorf("person %d: %w", pid, ErrNotCandidate)
			}
			return nil, fmt.Errorf("save poll choice %d: %w", i+1, err)
		}
	}
	if err := pollUpdated(ctx, tx, eventID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update poll ballot: %w", err)
	}
	if next == nil {
		next = []int64{}
	}
	return next, nil
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

// UserChoices returns every vote of the user, by event then rank (no card).
func (s *PGStore) UserChoices(ctx context.Context, userID int64) ([]UserChoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, rank, person_id FROM poll_choices
		WHERE user_id = $1 ORDER BY event_id, rank`, userID)
	if err != nil {
		return nil, fmt.Errorf("poll choices of user %d: %w", userID, err)
	}
	defer rows.Close()
	out := []UserChoice{}
	for rows.Next() {
		var c UserChoice
		if err := rows.Scan(&c.EventID, &c.Rank, &c.PersonID); err != nil {
			return nil, fmt.Errorf("scan poll choice: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("poll choices of user %d: %w", userID, err)
	}
	return out, nil
}

// MoveBallots carries the ballots and the consent of an account to another,
// in the accounts' transaction (accounts.Hooks.Moved): a verified identity
// moved, and what it voted goes with it. The new account has no ballot, as
// it could not vote without the identity.
func (s *PGStore) MoveBallots(ctx context.Context, tx pgx.Tx, from, to int64) error {
	rows, err := tx.Query(ctx, `UPDATE poll_choices SET user_id = $1 WHERE user_id = $2 RETURNING event_id`, to, from)
	if err != nil {
		return fmt.Errorf("move ballots: %w", err)
	}
	events, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("move ballots: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO poll_consents (user_id, consent_at)
		SELECT $1, consent_at FROM poll_consents WHERE user_id = $2
		ON CONFLICT (user_id) DO NOTHING`, to, from); err != nil {
		return fmt.Errorf("move consent: %w", err)
	}
	return logEvents(ctx, tx, events)
}

// ForgetUser logs a poll_updated event for each event the account voted in,
// before it is deleted with its ballots (accounts.Hooks.Deleting), so the
// open result pages refresh.
func (s *PGStore) ForgetUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT event_id FROM poll_choices WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("poll events of user %d: %w", userID, err)
	}
	events, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("poll events of user %d: %w", userID, err)
	}
	return logEvents(ctx, tx, events)
}

func logEvents(ctx context.Context, tx pgx.Tx, events []int64) error {
	seen := map[int64]bool{}
	for _, e := range events {
		if seen[e] {
			continue
		}
		seen[e] = true
		if err := pollUpdated(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}
