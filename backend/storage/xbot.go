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
// statement, the quotes of its sources (the first one in Quote), its date
// and its sources. The date is the statement's when the fact has one, with
// its precision ("year", "month", "day"), else the date of the first source
// ("": a full date).
type BotFact struct {
	ID            int64
	Text          string
	Quote         string
	Quotes        []string
	Date          *time.Time
	DatePrecision string
	SourceURLs    []string
}

// DateFR is the date as the site prints it, no more than is known: "2013",
// "mars 2024", "14 mars 2024"; "" without a date.
func (f BotFact) DateFR() string {
	if f.Date == nil {
		return ""
	}
	months := []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
	d := f.Date.UTC()
	switch f.DatePrecision {
	case "year":
		return fmt.Sprintf("%d", d.Year())
	case "month":
		return fmt.Sprintf("%s %d", months[d.Month()-1], d.Year())
	}
	day := fmt.Sprintf("%d", d.Day())
	if d.Day() == 1 {
		day = "1er"
	}
	return fmt.Sprintf("%s %s %d", day, months[d.Month()-1], d.Year())
}

// BotContradiction is a contradiction the X bot may tweet, with what the
// tweet and its tags need.
type BotContradiction struct {
	ID          int64
	PersonID    int64
	PersonName  string
	PersonSlug  string
	StanceLabel string
	// Reason is why the two facts conflict: the explanation the site shows,
	// else the judge's one line.
	Reason string
	A, B   BotFact
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
	XAccountCategories = []string{"party", "outlet", "journalist", "factcheck", "association", "independent", "personality", "other"}
	XAccountStatuses   = []string{"suggested", "approved", "refused"}
)

// PickBotContradictions chooses up to n contradictions of the candidates for
// the X bot, one per person: the persons the bot has not drafted on for the
// longest first (never drafted first, then at random), and for each a
// contradiction never drafted (a draft skipped, expired or refused never
// comes back) nor sharing a fact with one that was (the same story again),
// not reported, not reviewed false, the ones reviewed real first,
// then at random.
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
			  AND NOT EXISTS (SELECT 1 FROM x_posts xp WHERE xp.contradiction_id = c.id)
			  AND NOT EXISTS (
				SELECT 1 FROM x_posts xp JOIN contradictions c2 ON c2.id = xp.contradiction_id
				WHERE c2.fact_a_id IN (c.fact_a_id, c.fact_b_id) OR c2.fact_b_id IN (c.fact_a_id, c.fact_b_id))
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
		SELECT c.person_id, p.name, p.slug, COALESCE(sd.cluster_label, ''), COALESCE(NULLIF(c.explanation, ''), c.reason),
		       EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'real'),
		       fa.id, fa.fact_text, COALESCE(sa.quote, ''), `+shownDateSQL("fa", "sa.earliest")+`, COALESCE(fa.statement_date_precision, ''), COALESCE(sa.urls, '{}'),
		       ARRAY(SELECT original_quote FROM fact_sources WHERE fact_id = fa.id AND COALESCE(original_quote, '') <> '' ORDER BY published_at ASC NULLS LAST),
		       fb.id, fb.fact_text, COALESCE(sb.quote, ''), `+shownDateSQL("fb", "sb.earliest")+`, COALESCE(fb.statement_date_precision, ''), COALESCE(sb.urls, '{}'),
		       ARRAY(SELECT original_quote FROM fact_sources WHERE fact_id = fb.id AND COALESCE(original_quote, '') <> '' ORDER BY published_at ASC NULLS LAST)
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
		`+factSidesLateral("fa", "sa")+`
		`+factSidesLateral("fb", "sb")+`
		WHERE c.id = $1`, id).Scan(&c.PersonID, &c.PersonName, &c.PersonSlug, &c.StanceLabel, &reason, &c.Reviewed,
		&c.A.ID, &c.A.Text, &c.A.Quote, &c.A.Date, &c.A.DatePrecision, &c.A.SourceURLs, &c.A.Quotes,
		&c.B.ID, &c.B.Text, &c.B.Quote, &c.B.Date, &c.B.DatePrecision, &c.B.SourceURLs, &c.B.Quotes)
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

// VoteTagAccounts returns up to n approved accounts of the categories, that
// are not the account of a tracked person or party, the least recently
// tagged first (never tagged first, then at random).
func (s *PostgresStorer) VoteTagAccounts(ctx context.Context, categories []string, n int) ([]XAccount, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xAccountColumns+` FROM x_accounts
		WHERE status = 'approved' AND category = ANY($1) AND person_id IS NULL
		ORDER BY last_tagged_at NULLS FIRST, random() LIMIT $2`, categories, n)
	if err != nil {
		return nil, fmt.Errorf("vote tag accounts: %w", err)
	}
	return pgx.CollectRows(rows, scanXAccount)
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

// BotTagPool is what the tags of one tweet are chosen from: the accounts of
// the person and the approved accounts of the sites that published the
// facts, the least recently tagged first. Nobody else: a tag must be the
// person spoken of or the outlet whose report is quoted.
type BotTagPool struct {
	Person  []string
	Outlets []XAccount
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
	return pool, nil
}

// The kinds of a tweet of the bot: a contradiction of a candidate, or a call
// to the vote.
const (
	XPostContradiction = "contradiction"
	XPostVote          = "vote"
)

// XPost is a tweet of the X bot (x_posts). A tweet on the vote has no
// contradiction and no person (PersonID 0). QuoteTweetID is the tweet of the
// person it quotes, when one of the two facts is that tweet.
type XPost struct {
	ID              int64
	Kind            string
	ContradictionID *int64
	PersonID        int64
	QuoteTweetID    *string
	Slot            time.Time
	// PostAt is when the tweet goes out: the slot moved by the bot's random
	// offset (zero at the insert: the slot).
	PostAt   time.Time
	Variants []string
	Tags     []string
	// Angle is the angle of a tweet on the vote (xbot.VoteAngles; "": none).
	Angle string
	// FactID is the fact of a candidate a vote tweet starts from (nil: none).
	FactID            *int64
	Chosen            *string
	Status            string
	TelegramMessageID *int64
	TweetID           *string
	Error             string
	CreatedAt         time.Time
}

const xPostColumns = `id, kind, contradiction_id, COALESCE(person_id, 0), quote_tweet_id, slot, COALESCE(post_at, slot), variants, tags, angle, fact_id, chosen, status, telegram_message_id, tweet_id, error, created_at`

func scanXPost(row pgx.CollectableRow) (XPost, error) {
	var p XPost
	err := row.Scan(&p.ID, &p.Kind, &p.ContradictionID, &p.PersonID, &p.QuoteTweetID, &p.Slot, &p.PostAt, &p.Variants, &p.Tags, &p.Angle, &p.FactID, &p.Chosen, &p.Status,
		&p.TelegramMessageID, &p.TweetID, &p.Error, &p.CreatedAt)
	return p, err
}

// ErrNoXPost is returned when the tweet does not exist.
var ErrNoXPost = errors.New("no such tweet")

// InsertXPost stores a draft for its slot, and its versions among the texts
// the bot proposed (x_post_texts).
func (s *PostgresStorer) InsertXPost(ctx context.Context, p XPost) (int64, error) {
	if p.Variants == nil {
		p.Variants = []string{}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.Kind == "" {
		p.Kind = XPostContradiction
	}
	if p.PostAt.IsZero() {
		p.PostAt = p.Slot
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin insert x post: %w", err)
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO x_posts (kind, contradiction_id, person_id, quote_tweet_id, slot, post_at, variants, tags, angle, fact_id, status)
		VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, $7, $8, $9, $10, 'draft') RETURNING id`,
		p.Kind, p.ContradictionID, p.PersonID, p.QuoteTweetID, p.Slot, p.PostAt, p.Variants, p.Tags, p.Angle, p.FactID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert x post: %w", err)
	}
	if err := recordXPostTexts(ctx, tx, id, p.Variants); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit insert x post: %w", err)
	}
	return id, nil
}

// recordXPostTexts keeps the texts proposed for a tweet (x_post_texts): the
// bot never proposes one of them again.
func recordXPostTexts(ctx context.Context, tx pgx.Tx, postID int64, texts []string) error {
	if len(texts) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO x_post_texts (post_id, text) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, postID, texts); err != nil {
		return fmt.Errorf("x post %d texts: %w", postID, err)
	}
	return nil
}

// XPostTexts returns every text the bot proposed for the tweets of a kind,
// the newest first, tags and link included. exceptPost leaves the texts of
// that tweet out (0: none): a rewrite may stay close to its own draft.
func (s *PostgresStorer) XPostTexts(ctx context.Context, kind string, exceptPost int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.text FROM x_post_texts t JOIN x_posts p ON p.id = t.post_id
		WHERE p.kind = $1 AND p.id <> $2 ORDER BY t.id DESC`, kind, exceptPost)
	if err != nil {
		return nil, fmt.Errorf("x post texts: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// VoteAngleUses returns when each angle of the vote tweets was last drafted.
func (s *PostgresStorer) VoteAngleUses(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT angle, MAX(created_at) FROM x_posts WHERE kind = $1 AND angle <> '' GROUP BY angle`, XPostVote)
	if err != nil {
		return nil, fmt.Errorf("vote angle uses: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var angle string
		var at time.Time
		if err := rows.Scan(&angle, &at); err != nil {
			return nil, fmt.Errorf("scan vote angle use: %w", err)
		}
		out[angle] = at
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vote angle uses: %w", err)
	}
	return out, nil
}

// VoteQuoteTarget returns the latest tweet on a contradiction the bot posted
// since `since` that no vote tweet was drafted as a quote of (whatever became
// of that draft: a quote proposed once is not proposed again); nil when
// there is none.
func (s *PostgresStorer) VoteQuoteTarget(ctx context.Context, since time.Time) (*XPost, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xPostColumns+` FROM x_posts
		WHERE kind = $1 AND status = 'posted' AND tweet_id IS NOT NULL AND chosen IS NOT NULL AND posted_at >= $3
		  AND NOT EXISTS (SELECT 1 FROM x_posts v WHERE v.kind = $2 AND v.quote_tweet_id = x_posts.tweet_id)
		ORDER BY posted_at DESC LIMIT 1`, XPostContradiction, XPostVote, since)
	if err != nil {
		return nil, fmt.Errorf("vote quote target: %w", err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanXPost)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vote quote target: %w", err)
	}
	return &p, nil
}

// VoteFact is something a candidate said about the vote, democracy or the
// constitution: the occasion of a vote tweet that takes them at their word.
type VoteFact struct {
	BotFact
	PersonID   int64
	PersonName string
}

// VoteFactWords finds, in the quote of a source, the words of a statement on
// the vote and democracy (a case-insensitive regular expression).
const VoteFactWords = `(droit de vote|référendum|referendum|démocratie|constitution|abstention|proportionnelle|parole (au|aux) (peuple|français)|vote blanc|les électeurs|suffrage universel|(aller|allez|irai|iront) voter)`

const voteFactSelect = `
	SELECT f.id, f.fact_text, fs.original_quote, COALESCE(f.statement_date::timestamp AT TIME ZONE 'UTC', fs.published_at),
	       COALESCE(f.statement_date_precision, ''), fs.source_url, p.id, p.name
	FROM facts f
	JOIN fact_sources fs ON fs.fact_id = f.id
	JOIN fact_mentions fm ON fm.fact_source_id = fs.id
	JOIN persons p ON p.id = fm.person_id`

func scanVoteFact(row pgx.Row) (*VoteFact, error) {
	var f VoteFact
	var url string
	if err := row.Scan(&f.ID, &f.Text, &f.Quote, &f.Date, &f.DatePrecision, &url, &f.PersonID, &f.PersonName); err != nil {
		return nil, err
	}
	f.Quotes, f.SourceURLs = []string{f.Quote}, []string{url}
	return &f, nil
}

// PickVoteFact chooses what a candidate said about the vote or democracy
// (VoteFactWords in the quote of a source published since `since`) for a
// vote tweet: a fact no tweet was drafted from, not reported, of the
// candidate such a tweet was drafted on the longest ago (never first), the
// latest statement first. Nil when there is none.
func (s *PostgresStorer) PickVoteFact(ctx context.Context, since time.Time) (*VoteFact, error) {
	f, err := scanVoteFact(s.pool.QueryRow(ctx, voteFactSelect+`
		WHERE `+CandidateSQL+` AND p.entity_type = 'human'
		  AND char_length(fs.original_quote) BETWEEN 40 AND 400
		  AND fs.original_quote ~* $1 AND fs.published_at >= $2
		  AND NOT EXISTS (SELECT 1 FROM x_posts xp WHERE xp.fact_id = f.id)
		  AND NOT EXISTS (SELECT 1 FROM user_reports r WHERE r.target_kind = 'fact' AND r.target_id = f.id
			AND r.status IN ('pending', 'review', 'accepted'))
		ORDER BY (SELECT MAX(xp.created_at) FROM x_posts xp WHERE xp.fact_id IS NOT NULL AND xp.person_id = p.id) ASC NULLS FIRST,
		         fs.published_at DESC
		LIMIT 1`, VoteFactWords, since))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pick vote fact: %w", err)
	}
	return f, nil
}

// GetVoteFact loads the fact of a person a vote tweet was drafted from.
func (s *PostgresStorer) GetVoteFact(ctx context.Context, factID, personID int64) (*VoteFact, error) {
	f, err := scanVoteFact(s.pool.QueryRow(ctx, voteFactSelect+`
		WHERE f.id = $1 AND p.id = $2 AND COALESCE(fs.original_quote, '') <> ''
		ORDER BY (fs.original_quote ~* $3) DESC, fs.published_at DESC NULLS LAST LIMIT 1`, factID, personID, VoteFactWords))
	if err != nil {
		return nil, fmt.Errorf("vote fact %d: %w", factID, err)
	}
	return f, nil
}

// XPostByTweetID finds the tweet the bot posted under this id on X.
func (s *PostgresStorer) XPostByTweetID(ctx context.Context, tweetID string) (*XPost, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xPostColumns+` FROM x_posts WHERE tweet_id = $1 ORDER BY id DESC LIMIT 1`, tweetID)
	if err != nil {
		return nil, fmt.Errorf("x post of tweet %s: %w", tweetID, err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanXPost)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoXPost
	}
	if err != nil {
		return nil, fmt.Errorf("x post of tweet %s: %w", tweetID, err)
	}
	return &p, nil
}

// GetXPost loads a tweet.
func (s *PostgresStorer) GetXPost(ctx context.Context, id int64) (*XPost, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xPostColumns+` FROM x_posts WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("x post %d: %w", id, err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanXPost)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoXPost
	}
	if err != nil {
		return nil, fmt.Errorf("x post %d: %w", id, err)
	}
	return &p, nil
}

// XPostByTelegramMessage finds the tweet a Telegram message shows.
func (s *PostgresStorer) XPostByTelegramMessage(ctx context.Context, messageID int64) (*XPost, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM x_posts WHERE telegram_message_id = $1`, messageID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoXPost
	}
	if err != nil {
		return nil, fmt.Errorf("x post of telegram message %d: %w", messageID, err)
	}
	return s.GetXPost(ctx, id)
}

// XPostSlotTaken reports whether a tweet (of any status: a skipped slot is
// not drafted again) holds the slot.
func (s *PostgresStorer) XPostSlotTaken(ctx context.Context, slot time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM x_posts WHERE slot = $1)`, slot).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("x post slot: %w", err)
	}
	return ok, nil
}

// OpenXPostPersons returns the persons of the drafts and approved tweets not
// posted yet: a new draft is about someone else.
func (s *PostgresStorer) OpenXPostPersons(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT person_id FROM x_posts WHERE status IN ('draft', 'approved') AND person_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("open x post persons: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// SetXPostTelegram records the Telegram message that shows the draft.
func (s *PostgresStorer) SetXPostTelegram(ctx context.Context, id, messageID int64) error {
	if _, err := s.pool.Exec(ctx, `UPDATE x_posts SET telegram_message_id = $2 WHERE id = $1`, id, messageID); err != nil {
		return fmt.Errorf("x post %d telegram: %w", id, err)
	}
	return nil
}

// ReviseXPost gives a draft its new versions and the Telegram message that
// shows them, and keeps the versions among the texts proposed; it returns
// false when the tweet was no longer a draft.
func (s *PostgresStorer) ReviseXPost(ctx context.Context, id int64, variants []string, messageID int64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin revise x post %d: %w", id, err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE x_posts SET variants = $2, telegram_message_id = $3 WHERE id = $1 AND status = 'draft'`, id, variants, messageID)
	if err != nil {
		return false, fmt.Errorf("revise x post %d: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if err := recordXPostTexts(ctx, tx, id, variants); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit revise x post %d: %w", id, err)
	}
	return true, nil
}

// DecideXPost moves a draft to status (approved with its chosen text,
// skipped, false); it returns false when the tweet was no longer a draft. The
// chosen text is kept among the texts proposed (it may be the approver's
// own).
func (s *PostgresStorer) DecideXPost(ctx context.Context, id int64, status string, chosen *string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin decide x post %d: %w", id, err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE x_posts SET status = $2, chosen = COALESCE($3, chosen), decided_at = now()
		WHERE id = $1 AND status = 'draft'`, id, status, chosen)
	if err != nil {
		return false, fmt.Errorf("decide x post %d: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if chosen != nil {
		if err := recordXPostTexts(ctx, tx, id, []string{*chosen}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit decide x post %d: %w", id, err)
	}
	return true, nil
}

// DueXPosts returns the approved tweets whose time has come.
func (s *PostgresStorer) DueXPosts(ctx context.Context, now time.Time) ([]XPost, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+xPostColumns+` FROM x_posts WHERE status = 'approved' AND COALESCE(post_at, slot) <= $1 ORDER BY COALESCE(post_at, slot)`, now)
	if err != nil {
		return nil, fmt.Errorf("due x posts: %w", err)
	}
	return pgx.CollectRows(rows, scanXPost)
}

// MarkXPostPosted records the tweet posted and its tags as tagged now.
func (s *PostgresStorer) MarkXPostPosted(ctx context.Context, id int64, tweetID string, tags []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin x post posted: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE x_posts SET status = 'posted', tweet_id = $2, posted_at = now(), error = '' WHERE id = $1`, id, tweetID); err != nil {
		return fmt.Errorf("x post %d posted: %w", id, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE x_accounts SET last_tagged_at = now() WHERE handle = ANY($1)`, tags); err != nil {
		return fmt.Errorf("x post %d tags: %w", id, err)
	}
	return tx.Commit(ctx)
}

// MarkXPostFailed records why a tweet could not be posted.
func (s *PostgresStorer) MarkXPostFailed(ctx context.Context, id int64, reason string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE x_posts SET status = 'failed', error = $2 WHERE id = $1`, id, reason); err != nil {
		return fmt.Errorf("x post %d failed: %w", id, err)
	}
	return nil
}

// ExpireXPosts sets the drafts whose slot ended before `before` expired and
// returns them.
func (s *PostgresStorer) ExpireXPosts(ctx context.Context, before time.Time) ([]XPost, error) {
	rows, err := s.pool.Query(ctx, `UPDATE x_posts SET status = 'expired', decided_at = now()
		WHERE status = 'draft' AND slot < $1 RETURNING `+xPostColumns, before)
	if err != nil {
		return nil, fmt.Errorf("expire x posts: %w", err)
	}
	return pgx.CollectRows(rows, scanXPost)
}

// XOAuth is the X bot's OAuth 2.0 tokens (x_oauth): the refresh token in
// use, the access token it gave and until when, and the seed they started
// from.
type XOAuth struct {
	Seed         string
	RefreshToken string
	AccessToken  string
	ExpiresAt    time.Time
}

// GetXOAuth returns the stored tokens, nil when none were stored yet.
func (s *PostgresStorer) GetXOAuth(ctx context.Context) (*XOAuth, error) {
	var t XOAuth
	err := s.pool.QueryRow(ctx, `SELECT seed, refresh_token, access_token, expires_at FROM x_oauth`).
		Scan(&t.Seed, &t.RefreshToken, &t.AccessToken, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("x oauth: %w", err)
	}
	return &t, nil
}

// SaveXOAuth stores the tokens in place of the previous ones.
func (s *PostgresStorer) SaveXOAuth(ctx context.Context, t XOAuth) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO x_oauth (id, seed, refresh_token, access_token, expires_at, updated_at)
		VALUES (true, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE SET seed = EXCLUDED.seed, refresh_token = EXCLUDED.refresh_token,
			access_token = EXCLUDED.access_token, expires_at = EXCLUDED.expires_at, updated_at = now()`,
		t.Seed, t.RefreshToken, t.AccessToken, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save x oauth: %w", err)
	}
	return nil
}
