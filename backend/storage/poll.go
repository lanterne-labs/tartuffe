package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PollEvent is one pre-poll (the presidential election first): the users rank
// up to MaxChoices of its candidates.
type PollEvent struct {
	ID            int64          `json:"id"`
	Slug          string         `json:"slug"`
	TitleFR       string         `json:"title_fr"`
	TitleEN       string         `json:"title_en"`
	DescriptionFR string         `json:"description_fr"`
	DescriptionEN string         `json:"description_en"`
	MaxChoices    int            `json:"max_choices"`
	MinVoters     int            `json:"min_voters"`
	OpensAt       time.Time      `json:"opens_at"`
	ClosesAt      *time.Time     `json:"closes_at,omitempty"`
	Featured      bool           `json:"featured"`
	Blackouts     []PollBlackout `json:"blackouts"`
}

// PollBlackout is a period when the results of an event are not shown.
type PollBlackout struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// PollCandidateTally is a candidate of an event with its honesty record and
// how many ballots rank them first, and at all.
type PollCandidateTally struct {
	PersonID           int64
	Name               string
	ImageURL           *string
	HonestyScore       float64
	FactCount          int
	ContradictionCount int
	FirstChoices       int
	Chosen             int
}

// AppUser is a signed-in user: one identity of one account provider.
// Verified says a passport proof is attached: the account may vote.
type AppUser struct {
	ID          int64      `json:"id"`
	Provider    string     `json:"provider"`
	DisplayName string     `json:"display_name"`
	ConsentAt   *time.Time `json:"consent_at,omitempty"`
	Verified    bool       `json:"verified"`
}

// appUserColumns read an AppUser of the alias u.
const appUserColumns = `u.id, u.provider, u.display_name, u.consent_at, u.verified_subject IS NOT NULL`

func scanAppUser(row pgx.Row) (*AppUser, error) {
	var u AppUser
	if err := row.Scan(&u.ID, &u.Provider, &u.DisplayName, &u.ConsentAt, &u.Verified); err != nil {
		return nil, err
	}
	return &u, nil
}

const pollEventColumns = `id, slug, title_fr, title_en, description_fr, description_en, max_choices, min_voters, opens_at, closes_at, featured`

func scanPollEvent(row pgx.Row) (*PollEvent, error) {
	var e PollEvent
	if err := row.Scan(&e.ID, &e.Slug, &e.TitleFR, &e.TitleEN, &e.DescriptionFR, &e.DescriptionEN,
		&e.MaxChoices, &e.MinVoters, &e.OpensAt, &e.ClosesAt, &e.Featured); err != nil {
		return nil, err
	}
	e.Blackouts = []PollBlackout{}
	return &e, nil
}

// ListPollEvents returns every event, the featured ones first, then the newest.
func (s *PostgresStorer) ListPollEvents(ctx context.Context) ([]PollEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pollEventColumns+` FROM poll_events ORDER BY featured DESC, opens_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list poll events: %w", err)
	}
	defer rows.Close()
	events := []PollEvent{}
	for rows.Next() {
		e, err := scanPollEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan poll event: %w", err)
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list poll events: %w", err)
	}
	for i := range events {
		if events[i].Blackouts, err = s.pollBlackouts(ctx, events[i].ID); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// GetPollEvent returns the event of that slug, or nil.
func (s *PostgresStorer) GetPollEvent(ctx context.Context, slug string) (*PollEvent, error) {
	e, err := scanPollEvent(s.pool.QueryRow(ctx, `SELECT `+pollEventColumns+` FROM poll_events WHERE slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get poll event %s: %w", slug, err)
	}
	if e.Blackouts, err = s.pollBlackouts(ctx, e.ID); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *PostgresStorer) pollBlackouts(ctx context.Context, eventID int64) ([]PollBlackout, error) {
	rows, err := s.pool.Query(ctx, `SELECT starts_at, ends_at FROM poll_blackouts WHERE event_id = $1 ORDER BY starts_at`, eventID)
	if err != nil {
		return nil, fmt.Errorf("poll blackouts of event %d: %w", eventID, err)
	}
	defer rows.Close()
	out := []PollBlackout{}
	for rows.Next() {
		var b PollBlackout
		if err := rows.Scan(&b.StartsAt, &b.EndsAt); err != nil {
			return nil, fmt.Errorf("scan poll blackout: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SavePollEvent creates the event of e.Slug or updates it, blackouts left
// as they are, and returns its id. A featured event unfeatures the others.
func (s *PostgresStorer) SavePollEvent(ctx context.Context, e PollEvent) (int64, error) {
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

// AddPollBlackout hides the results of the event over [from, to).
func (s *PostgresStorer) AddPollBlackout(ctx context.Context, eventID int64, from, to time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO poll_blackouts (event_id, starts_at, ends_at) VALUES ($1, $2, $3)
		ON CONFLICT (event_id, starts_at) DO UPDATE SET ends_at = EXCLUDED.ends_at`, eventID, from, to)
	if err != nil {
		return fmt.Errorf("add poll blackout: %w", err)
	}
	return nil
}

// ClearPollBlackouts removes every blackout of the event.
func (s *PostgresStorer) ClearPollBlackouts(ctx context.Context, eventID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM poll_blackouts WHERE event_id = $1`, eventID)
	if err != nil {
		return 0, fmt.Errorf("clear poll blackouts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AddPollCandidate makes the person a candidate of the event; false when they
// already were.
func (s *PostgresStorer) AddPollCandidate(ctx context.Context, eventID, personID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO poll_candidates (event_id, person_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, eventID, personID)
	if err != nil {
		return false, fmt.Errorf("add poll candidate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RemovePollCandidate removes the candidate and their place on every ballot;
// the ballots that ranked them below keep their order.
func (s *PostgresStorer) RemovePollCandidate(ctx context.Context, eventID, personID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM poll_candidates WHERE event_id = $1 AND person_id = $2`, eventID, personID)
	if err != nil {
		return false, fmt.Errorf("remove poll candidate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetPollCandidateIDs returns the person ids of the event's candidates.
func (s *PostgresStorer) GetPollCandidateIDs(ctx context.Context, eventID int64) ([]int64, error) {
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

// GetPollTallies returns the candidates of the event with their counts, and
// the number of ballots. A ballot's first choice is its lowest rank, so a
// ballot whose rank 1 candidate was removed counts for its next one.
func (s *PostgresStorer) GetPollTallies(ctx context.Context, eventID int64) ([]PollCandidateTally, int, error) {
	rows, err := s.pool.Query(ctx, `
		WITH firsts AS (
			SELECT DISTINCT ON (user_id) user_id, person_id
			FROM poll_choices WHERE event_id = $1
			ORDER BY user_id, rank
		)
		SELECT p.id, p.name, p.image_url, p.honesty_score, p.fact_count,
			(SELECT COUNT(*) FROM contradictions c WHERE c.person_id = p.id),
			(SELECT COUNT(*) FROM firsts f WHERE f.person_id = p.id),
			(SELECT COUNT(*) FROM poll_choices pc WHERE pc.event_id = $1 AND pc.person_id = p.id)
		FROM poll_candidates pc0
		JOIN persons p ON p.id = pc0.person_id
		WHERE pc0.event_id = $1
		ORDER BY p.name`, eventID)
	if err != nil {
		return nil, 0, fmt.Errorf("poll tallies of event %d: %w", eventID, err)
	}
	defer rows.Close()
	out := []PollCandidateTally{}
	for rows.Next() {
		var c PollCandidateTally
		if err := rows.Scan(&c.PersonID, &c.Name, &c.ImageURL, &c.HonestyScore, &c.FactCount,
			&c.ContradictionCount, &c.FirstChoices, &c.Chosen); err != nil {
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

// GetPollBallot returns the person ids the user ranked, first choice first.
func (s *PostgresStorer) GetPollBallot(ctx context.Context, eventID, userID int64) ([]int64, error) {
	return pollBallot(ctx, s.pool, eventID, userID)
}

func pollBallot(ctx context.Context, q interface {
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

// ErrNotCandidate is returned when a ballot names a person who is not (or
// no longer) a candidate of the event.
var ErrNotCandidate = errors.New("not a candidate of the event")

// UpdatePollBallot changes the user's ballot (person ids, first choice first)
// in one transaction: edit gets the ballot as stored and returns the new one
// (empty withdraws it), or an error that cancels the change and is returned
// as is. The user's row is locked first, so two requests of one user (two
// tabs, a double click) apply one after the other, each on the other's
// result. A changed ballot records the user's consent when consent is set,
// and logs a poll_updated event so the open result pages refresh. It returns
// the ballot as stored.
func (s *PostgresStorer) UpdatePollBallot(ctx context.Context, eventID, userID int64, consent bool, edit func(current []int64) ([]int64, error)) ([]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update poll ballot: %w", err)
	}
	defer tx.Rollback(ctx)
	// The lock also reads whether the account still holds a passport: a
	// passport moved to another account takes the right to vote with it.
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT verified_subject IS NOT NULL FROM app_users WHERE id = $1 FOR UPDATE`, userID).Scan(&verified); err != nil {
		return nil, fmt.Errorf("lock app user %d: %w", userID, err)
	}
	current, err := pollBallot(ctx, tx, eventID, userID)
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
		if _, err := tx.Exec(ctx, `UPDATE app_users SET consent_at = COALESCE(consent_at, now()) WHERE id = $1`, userID); err != nil {
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
	if err := s.insertEvent(ctx, tx, "poll_updated", "poll_event", eventID, map[string]interface{}{}); err != nil {
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

// UserPollChoice is one vote of a user, with the candidate it goes to.
type UserPollChoice struct {
	EventID      int64   `json:"event_id"`
	Rank         int     `json:"rank"`
	PersonID     int64   `json:"person_id"`
	Name         string  `json:"name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	FactCount    int     `json:"fact_count"`
}

// GetUserPollChoices returns every vote of the user, by event then rank.
func (s *PostgresStorer) GetUserPollChoices(ctx context.Context, userID int64) ([]UserPollChoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pc.event_id, pc.rank, p.id, p.name, p.image_url, p.honesty_score, p.fact_count
		FROM poll_choices pc JOIN persons p ON p.id = pc.person_id
		WHERE pc.user_id = $1
		ORDER BY pc.event_id, pc.rank`, userID)
	if err != nil {
		return nil, fmt.Errorf("poll choices of user %d: %w", userID, err)
	}
	defer rows.Close()
	out := []UserPollChoice{}
	for rows.Next() {
		var c UserPollChoice
		if err := rows.Scan(&c.EventID, &c.Rank, &c.PersonID, &c.Name, &c.ImageURL, &c.HonestyScore, &c.FactCount); err != nil {
			return nil, fmt.Errorf("scan poll choice: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("poll choices of user %d: %w", userID, err)
	}
	return out, nil
}

// DeleteAppUser erases the user with their sessions and ballots (the right to
// erasure: nothing of theirs is kept), and logs a poll_updated event for each
// event they had voted in so the open result pages refresh.
func (s *PostgresStorer) DeleteAppUser(ctx context.Context, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete app user: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT DISTINCT event_id FROM poll_choices WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("poll events of user %d: %w", userID, err)
	}
	events, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("poll events of user %d: %w", userID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_users WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("delete app user %d: %w", userID, err)
	}
	for _, id := range events {
		if err := s.insertEvent(ctx, tx, "poll_updated", "poll_event", id, map[string]interface{}{}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpsertAppUser returns the user of the identity, created on first sign-in.
func (s *PostgresStorer) UpsertAppUser(ctx context.Context, provider, subject, displayName string) (*AppUser, error) {
	u, err := scanAppUser(s.pool.QueryRow(ctx, `
		INSERT INTO app_users AS u (provider, subject, display_name) VALUES ($1, $2, $3)
		ON CONFLICT (provider, subject) DO UPDATE SET last_seen_at = now()
		RETURNING `+appUserColumns, provider, subject, displayName))
	if err != nil {
		return nil, fmt.Errorf("upsert app user: %w", err)
	}
	return u, nil
}

// CreateUserSession stores a session of the user by its token hash.
func (s *PostgresStorer) CreateUserSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt); err != nil {
		return fmt.Errorf("create user session: %w", err)
	}
	return nil
}

// GetSessionUser returns the user of a live session, or nil.
func (s *PostgresStorer) GetSessionUser(ctx context.Context, tokenHash []byte) (*AppUser, error) {
	u, err := scanAppUser(s.pool.QueryRow(ctx, `
		SELECT `+appUserColumns+`
		FROM user_sessions us JOIN app_users u ON u.id = us.user_id
		WHERE us.token_hash = $1 AND us.expires_at > now()`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session user: %w", err)
	}
	return u, nil
}

// DeleteUserSession ends a session; expired sessions are dropped on the way.
func (s *PostgresStorer) DeleteUserSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1 OR expires_at <= now()`, tokenHash); err != nil {
		return fmt.Errorf("delete user session: %w", err)
	}
	return nil
}

// PurgeAppUsers deletes the users of a provider with their sessions and
// ballots (for instance the noop test users before a launch).
func (s *PostgresStorer) PurgeAppUsers(ctx context.Context, provider string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM app_users WHERE provider = $1`, provider)
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	return tag.RowsAffected(), nil
}

// CreateAuthChallenge stores a one-time sign-in challenge of the provider;
// expired challenges are dropped on the way.
func (s *PostgresStorer) CreateAuthChallenge(ctx context.Context, provider, challenge string, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE expires_at <= now()`); err != nil {
		return fmt.Errorf("drop expired auth challenges: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_challenges (challenge, provider, expires_at) VALUES ($1, $2, $3)`,
		challenge, provider, expiresAt); err != nil {
		return fmt.Errorf("create auth challenge: %w", err)
	}
	return nil
}

// ConsumeAuthChallenge deletes the challenge and tells whether it was a live
// challenge of the provider: a challenge serves once.
func (s *PostgresStorer) ConsumeAuthChallenge(ctx context.Context, provider, challenge string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE challenge = $1 AND provider = $2 AND expires_at > now()`,
		challenge, provider)
	if err != nil {
		return false, fmt.Errorf("consume auth challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
