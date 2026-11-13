package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// XPostReply is the kind of a reply the bot proposes under someone's tweet
// (x_posts). It is posted by hand: no status of it is ever posted through
// the API.
const XPostReply = "reply"

// CandidateHandle is an X account of a candidate.
type CandidateHandle struct {
	PersonID int64
	Name     string
	Handle   string
}

// CandidateXHandles returns the active X handles of the candidates,
// lowercased.
func (s *PostgresStorer) CandidateXHandles(ctx context.Context) ([]CandidateHandle, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, lower(h.handle) FROM twitter_handles h JOIN persons p ON p.id = h.person_id
		WHERE h.active AND `+CandidateSQL+` AND p.entity_type = 'human' ORDER BY p.id, h.id`)
	if err != nil {
		return nil, fmt.Errorf("candidate x handles: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (CandidateHandle, error) {
		var h CandidateHandle
		err := row.Scan(&h.PersonID, &h.Name, &h.Handle)
		return h, err
	})
}

// ReplyTarget is a tweet the bot considered answering (x_reply_targets).
type ReplyTarget struct {
	TweetID   string
	PersonID  int64
	Author    string
	Text      string
	URL       string
	Views     int
	TweetedAt *time.Time
	// Outcome is "drafted" or "no_match" (no contradiction fits the tweet).
	Outcome string
}

// SeenReplyTargets returns which of the tweets were considered before.
func (s *PostgresStorer) SeenReplyTargets(ctx context.Context, tweetIDs []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT tweet_id FROM x_reply_targets WHERE tweet_id = ANY($1)`, tweetIDs)
	if err != nil {
		return nil, fmt.Errorf("seen reply targets: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("seen reply targets: %w", err)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	return seen, nil
}

// RecordReplyTarget keeps a tweet as considered, with its outcome.
func (s *PostgresStorer) RecordReplyTarget(ctx context.Context, t ReplyTarget) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO x_reply_targets (tweet_id, person_id, author, text, url, views, tweeted_at, outcome)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tweet_id) DO UPDATE SET outcome = EXCLUDED.outcome, views = EXCLUDED.views`,
		t.TweetID, t.PersonID, t.Author, t.Text, t.URL, t.Views, t.TweetedAt, t.Outcome)
	if err != nil {
		return fmt.Errorf("record reply target %s: %w", t.TweetID, err)
	}
	return nil
}

// ErrNoReplyTarget is returned when the tweet was never considered.
var ErrNoReplyTarget = errors.New("no such reply target")

// GetReplyTarget loads a tweet the bot considered.
func (s *PostgresStorer) GetReplyTarget(ctx context.Context, tweetID string) (*ReplyTarget, error) {
	var t ReplyTarget
	err := s.pool.QueryRow(ctx, `SELECT tweet_id, person_id, author, text, url, views, tweeted_at, outcome FROM x_reply_targets WHERE tweet_id = $1`, tweetID).
		Scan(&t.TweetID, &t.PersonID, &t.Author, &t.Text, &t.URL, &t.Views, &t.TweetedAt, &t.Outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoReplyTarget
	}
	if err != nil {
		return nil, fmt.Errorf("reply target %s: %w", tweetID, err)
	}
	return &t, nil
}

// ReplyChoice is a contradiction a reply may be about, as the chooser reads
// it: its subject, its two statements and why they conflict.
type ReplyChoice struct {
	ID          int64
	StanceLabel string
	A, B        string
	Reason      string
}

// ReplyChoices returns up to n contradictions of a person a reply may be
// about: not reviewed false, not reported, not used in a reply before; the
// ones reviewed real first, then the newest.
func (s *PostgresStorer) ReplyChoices(ctx context.Context, personID int64, n int) ([]ReplyChoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, COALESCE(sd.cluster_label, ''), fa.fact_text, fb.fact_text, COALESCE(NULLIF(c.explanation, ''), c.reason, '')
		FROM contradictions c
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
		WHERE c.person_id = $1
		  AND NOT EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'false')
		  AND NOT EXISTS (
			SELECT 1 FROM user_reports r
			WHERE r.status IN ('pending', 'review', 'accepted')
			  AND ((r.target_kind = 'contradiction' AND r.target_id = c.id)
			    OR (r.target_kind = 'fact' AND r.target_id IN (c.fact_a_id, c.fact_b_id))))
		  AND NOT EXISTS (SELECT 1 FROM x_posts xp WHERE xp.contradiction_id = c.id AND xp.kind = $3)
		ORDER BY EXISTS (SELECT 1 FROM contradiction_reviews cr WHERE cr.contradiction_id = c.id AND cr.verdict = 'real') DESC,
		         c.detected_at DESC NULLS LAST
		LIMIT $2`, personID, n, XPostReply)
	if err != nil {
		return nil, fmt.Errorf("reply choices of person %d: %w", personID, err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReplyChoice, error) {
		var c ReplyChoice
		err := row.Scan(&c.ID, &c.StanceLabel, &c.A, &c.B, &c.Reason)
		return c, err
	})
}

// MarkXPostReplied records that the reply of a draft was posted by hand; it
// returns false when the draft was already decided.
func (s *PostgresStorer) MarkXPostReplied(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE x_posts SET status = 'posted', decided_at = now(), posted_at = now()
		WHERE id = $1 AND status = 'draft' AND kind = $2`, id, XPostReply)
	if err != nil {
		return false, fmt.Errorf("x post %d replied: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}
