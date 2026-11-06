package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// BotFact is one side of a contradiction the X bot drafts a tweet on: the
// statement, its first source quote, its date and its sources.
type BotFact struct {
	ID         int64
	Text       string
	Quote      string
	Date       *time.Time
	SourceURLs []string
}

// BotContradiction is a contradiction the X bot may tweet, with what the
// tweet and its tags need.
type BotContradiction struct {
	ID          int64
	PersonID    int64
	PersonName  string
	StanceLabel string
	Reason      string
	A, B        BotFact
	// Reviewed is true when a person reviewed it as real.
	Reviewed bool
}

// XAccount is an account the X bot may tag (x_accounts).
type XAccount struct {
	ID           int64
	Handle       string
	Name         string
	Category     string
	PersonID     *int64
	Domain       *string
	Status       string
	Note         string
	LastTaggedAt *time.Time
}

// The categories and statuses of x_accounts.
var (
	XAccountCategories = []string{"party", "outlet", "journalist", "factcheck", "association", "independent", "other"}
	XAccountStatuses   = []string{"suggested", "approved", "refused"}
)

// PickBotContradictions chooses up to n contradictions of the candidates for
// the X bot, one per person: the persons the bot has not drafted on for the
// longest first (never drafted first, then at random), and for each a
// contradiction not drafted yet (a skipped draft may come back), not
// reported, not reviewed false, the ones reviewed real first, then at random.
// excludePersons leaves those persons out (a page of drafts already holding
// them).
func (s *PostgresStorer) PickBotContradictions(ctx context.Context, n int, excludePersons []int64) ([]BotContradiction, error) {
	if excludePersons == nil {
		excludePersons = []int64{}
	}
	rows, err := s.pool.Query(ctx, `
		WITH eligible AS (
			SELECT c.id, c.person_id,
			       EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'real') AS reviewed
			FROM contradictions c
			JOIN persons p ON p.id = c.person_id
			WHERE `+CandidateSQL+` AND p.entity_type = 'human'
			  AND c.person_id <> ALL($2)
			  AND NOT EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'false')
			  AND NOT EXISTS (
				SELECT 1 FROM user_reports r
				WHERE r.status IN ('pending', 'review', 'accepted')
				  AND ((r.target_kind = 'contradiction' AND r.target_id = c.id)
				    OR (r.target_kind = 'fact' AND r.target_id IN (c.fact_a_id, c.fact_b_id))))
			  AND NOT EXISTS (SELECT 1 FROM x_posts xp WHERE xp.contradiction_id = c.id AND xp.status <> 'skipped')
		), one AS (
			SELECT DISTINCT ON (person_id) id, person_id, reviewed
			FROM eligible
			ORDER BY person_id, reviewed DESC, random()
		)
		SELECT one.id, one.reviewed
		FROM one
		LEFT JOIN LATERAL (
			SELECT MAX(created_at) AS at FROM x_posts xp
			WHERE xp.person_id = one.person_id AND xp.status IN ('draft', 'approved', 'posted')
		) last ON true
		ORDER BY last.at ASC NULLS FIRST, random()
		LIMIT $1`, n, excludePersons)
	if err != nil {
		return nil, fmt.Errorf("pick bot contradictions: %w", err)
	}
	type picked struct {
		id       int64
		reviewed bool
	}
	var ids []picked
	for rows.Next() {
		var p picked
		if err := rows.Scan(&p.id, &p.reviewed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bot contradiction: %w", err)
		}
		ids = append(ids, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pick bot contradictions: %w", err)
	}
	out := make([]BotContradiction, 0, len(ids))
	for _, p := range ids {
		c, err := s.GetBotContradiction(ctx, p.id)
		if err != nil {
			return nil, err
		}
		c.Reviewed = p.reviewed
		out = append(out, *c)
	}
	return out, nil
}

// ErrNoBotContradiction is returned when the contradiction does not exist.
var ErrNoBotContradiction = errors.New("no such contradiction")

// GetBotContradiction loads a contradiction with its two facts, the earlier
// one first.
func (s *PostgresStorer) GetBotContradiction(ctx context.Context, id int64) (*BotContradiction, error) {
	c := BotContradiction{ID: id}
	var reason *string
	err := s.pool.QueryRow(ctx, `
		SELECT c.person_id, p.name, COALESCE(sd.cluster_label, ''), c.reason,
		       EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'real'),
		       fa.id, fa.fact_text, COALESCE(sa.quote, ''), sa.earliest, COALESCE(sa.urls, '{}'),
		       fb.id, fb.fact_text, COALESCE(sb.quote, ''), sb.earliest, COALESCE(sb.urls, '{}')
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
		`+factSidesLateral("fa", "sa")+`
		`+factSidesLateral("fb", "sb")+`
		WHERE c.id = $1`, id).Scan(&c.PersonID, &c.PersonName, &c.StanceLabel, &reason, &c.Reviewed,
		&c.A.ID, &c.A.Text, &c.A.Quote, &c.A.Date, &c.A.SourceURLs,
		&c.B.ID, &c.B.Text, &c.B.Quote, &c.B.Date, &c.B.SourceURLs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoBotContradiction
	}
	if err != nil {
		return nil, fmt.Errorf("bot contradiction %d: %w", id, err)
	}
	if reason != nil {
		c.Reason = *reason
	}
	if c.B.Date != nil && c.A.Date != nil && c.B.Date.Before(*c.A.Date) {
		c.A, c.B = c.B, c.A
	}
	return &c, nil
}

// PersonXHandles returns the active X handles of a person (twitter_handles,
// the accounts the twitter plugin reads), lowercased.
func (s *PostgresStorer) PersonXHandles(ctx context.Context, personID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT lower(handle) FROM twitter_handles WHERE person_id = $1 AND active ORDER BY id`, personID)
	if err != nil {
		return nil, fmt.Errorf("x handles of person %d: %w", personID, err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// PersonParties returns the ids of the parties a person belongs to today.
func (s *PostgresStorer) PersonParties(ctx context.Context, personID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT pp.id FROM person_links pl
		JOIN persons pp ON pp.id = pl.dst_person_id
		WHERE pl.src_person_id = $1 AND pp.entity_type = 'party'
		  AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)`, personID)
	if err != nil {
		return nil, fmt.Errorf("parties of person %d: %w", personID, err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

const xAccountColumns = `id, handle, name, category, person_id, domain, status, note, last_tagged_at`

func scanXAccount(row pgx.CollectableRow) (XAccount, error) {
	var a XAccount
	err := row.Scan(&a.ID, &a.Handle, &a.Name, &a.Category, &a.PersonID, &a.Domain, &a.Status, &a.Note, &a.LastTaggedAt)
	return a, err
}

// ListXAccounts lists the accounts of a status ("": all), by category and
// handle.
func (s *PostgresStorer) ListXAccounts(ctx context.Context, status string) ([]XAccount, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts
		WHERE ($1 = '' OR status = $1) ORDER BY category, handle`, status)
	if err != nil {
		return nil, fmt.Errorf("list x accounts: %w", err)
	}
	return pgx.CollectRows(rows, scanXAccount)
}

// NormalizeXHandle lowercases a handle and drops its @ and spaces.
func NormalizeXHandle(h string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(h), "@"))
}

// UpsertXAccount adds an account, or updates the given fields of an existing
// one (same handle): name, category, person, domain and note when set; the
// status only when given ("": a new account is suggested, an existing one
// keeps its own).
func (s *PostgresStorer) UpsertXAccount(ctx context.Context, a XAccount) (XAccount, error) {
	a.Handle = NormalizeXHandle(a.Handle)
	if a.Handle == "" {
		return XAccount{}, fmt.Errorf("x account without a handle")
	}
	if a.Domain != nil {
		d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(*a.Domain)), "www.")
		a.Domain = &d
	}
	rows, err := s.pool.Query(ctx, `
		INSERT INTO x_accounts (handle, name, category, person_id, domain, status, note)
		VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'other'), $4, $5, COALESCE(NULLIF($6, ''), 'suggested'), $7)
		ON CONFLICT (handle) DO UPDATE SET
			name = COALESCE(NULLIF(EXCLUDED.name, ''), x_accounts.name),
			category = COALESCE(NULLIF($3, ''), x_accounts.category),
			person_id = COALESCE(EXCLUDED.person_id, x_accounts.person_id),
			domain = COALESCE(EXCLUDED.domain, x_accounts.domain),
			status = COALESCE(NULLIF($6, ''), x_accounts.status),
			note = COALESCE(NULLIF(EXCLUDED.note, ''), x_accounts.note)
		RETURNING `+xAccountColumns, a.Handle, a.Name, a.Category, a.PersonID, a.Domain, a.Status, a.Note)
	if err != nil {
		return XAccount{}, fmt.Errorf("upsert x account %s: %w", a.Handle, err)
	}
	return pgx.CollectExactlyOneRow(rows, scanXAccount)
}

// SetXAccountStatus sets the status of accounts by handle; it returns how
// many were found.
func (s *PostgresStorer) SetXAccountStatus(ctx context.Context, status string, handles []string) (int64, error) {
	norm := make([]string, len(handles))
	for i, h := range handles {
		norm[i] = NormalizeXHandle(h)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE x_accounts SET status = $1 WHERE handle = ANY($2)`, status, norm)
	if err != nil {
		return 0, fmt.Errorf("set x account status: %w", err)
	}
	return tag.RowsAffected(), nil
}

// BotTagPool is what the tags of one tweet are chosen from: the approved
// accounts of the person and their parties, of the sites of the facts'
// sources, and the approved others, the least recently tagged first.
type BotTagPool struct {
	Person  []string
	Parties []XAccount
	Outlets []XAccount
	Others  []XAccount
}

// BotTagPool gathers the accounts a tweet on the contradiction may tag.
func (s *PostgresStorer) BotTagPool(ctx context.Context, c *BotContradiction, domains []string) (*BotTagPool, error) {
	pool := &BotTagPool{}
	var err error
	if pool.Person, err = s.PersonXHandles(ctx, c.PersonID); err != nil {
		return nil, err
	}
	// An account of the person in x_accounts counts too (a refused one is
	// never tagged, even from twitter_handles).
	rows, err := s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts WHERE person_id = $1`, c.PersonID)
	if err != nil {
		return nil, fmt.Errorf("x accounts of person %d: %w", c.PersonID, err)
	}
	own, err := pgx.CollectRows(rows, scanXAccount)
	if err != nil {
		return nil, fmt.Errorf("x accounts of person %d: %w", c.PersonID, err)
	}
	refused := map[string]bool{}
	for _, a := range own {
		if a.Status == "refused" {
			refused[a.Handle] = true
		} else if a.Status == "approved" {
			pool.Person = append(pool.Person, a.Handle)
		}
	}
	person := pool.Person[:0]
	seen := map[string]bool{}
	for _, h := range pool.Person {
		if !refused[h] && !seen[h] {
			seen[h] = true
			person = append(person, h)
		}
	}
	pool.Person = person
	parties, err := s.PersonParties(ctx, c.PersonID)
	if err != nil {
		return nil, err
	}
	if len(parties) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts
			WHERE status = 'approved' AND person_id = ANY($1) ORDER BY last_tagged_at ASC NULLS FIRST, random()`, parties)
		if err != nil {
			return nil, fmt.Errorf("party x accounts: %w", err)
		}
		if pool.Parties, err = pgx.CollectRows(rows, scanXAccount); err != nil {
			return nil, fmt.Errorf("party x accounts: %w", err)
		}
	}
	if len(domains) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts
			WHERE status = 'approved' AND domain = ANY($1) ORDER BY last_tagged_at ASC NULLS FIRST, random()`, domains)
		if err != nil {
			return nil, fmt.Errorf("outlet x accounts: %w", err)
		}
		if pool.Outlets, err = pgx.CollectRows(rows, scanXAccount); err != nil {
			return nil, fmt.Errorf("outlet x accounts: %w", err)
		}
	}
	rows, err = s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts
		WHERE status = 'approved' AND category IN ('factcheck', 'association', 'independent', 'journalist', 'outlet')
		  AND person_id IS NULL
		ORDER BY last_tagged_at ASC NULLS FIRST, random() LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("other x accounts: %w", err)
	}
	if pool.Others, err = pgx.CollectRows(rows, scanXAccount); err != nil {
		return nil, fmt.Errorf("other x accounts: %w", err)
	}
	return pool, nil
}
