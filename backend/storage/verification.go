package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNotVerified is returned when an account without a passport proof adds
// a vote.
var ErrNotVerified = errors.New("the account has no passport proof")

// ErrOtherDocument is returned when an account that holds a passport is
// shown another one.
var ErrOtherDocument = errors.New("the account holds another document")

// AttachVerification gives the account the passport subject ("<provider>:<id>").
// When another account holds it, the passport moves: its ballots and its
// consent go to this account, and the other account loses the right to vote.
// It returns the account it moved from (0: none). Showing the account's own
// passport again changes nothing; another document is ErrOtherDocument.
func (s *PostgresStorer) AttachVerification(ctx context.Context, userID int64, subject string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("attach verification: %w", err)
	}
	defer tx.Rollback(ctx)

	// The holder of the passport, if any, and this account, locked in the
	// order of their ids so two moves never wait on each other.
	var from int64
	err = tx.QueryRow(ctx, `SELECT id FROM app_users WHERE verified_subject = $1`, subject).Scan(&from)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find passport holder: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM app_users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int64{userID, from}); err != nil {
		return 0, fmt.Errorf("lock accounts: %w", err)
	}
	var mine *string
	if err := tx.QueryRow(ctx, `SELECT verified_subject FROM app_users WHERE id = $1`, userID).Scan(&mine); err != nil {
		return 0, fmt.Errorf("read account %d: %w", userID, err)
	}
	if mine != nil {
		if *mine == subject {
			return 0, nil
		}
		return 0, ErrOtherDocument
	}
	// Read again under the locks: the holder may have changed.
	from = 0
	err = tx.QueryRow(ctx, `SELECT id FROM app_users WHERE verified_subject = $1 FOR UPDATE`, subject).Scan(&from)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find passport holder: %w", err)
	}
	if from != 0 {
		// This account has no ballot: it could not vote without a passport.
		rows, err := tx.Query(ctx, `
			UPDATE poll_choices SET user_id = $1 WHERE user_id = $2 RETURNING event_id`, userID, from)
		if err != nil {
			return 0, fmt.Errorf("move ballots: %w", err)
		}
		events, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return 0, fmt.Errorf("move ballots: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app_users SET consent_at = COALESCE(app_users.consent_at, o.consent_at)
			FROM app_users o WHERE app_users.id = $1 AND o.id = $2`, userID, from); err != nil {
			return 0, fmt.Errorf("move consent: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app_users SET verified_subject = NULL, verified_at = NULL WHERE id = $1`, from); err != nil {
			return 0, fmt.Errorf("detach passport from %d: %w", from, err)
		}
		seen := map[int64]bool{}
		for _, e := range events {
			if seen[e] {
				continue
			}
			seen[e] = true
			if err := s.insertEvent(ctx, tx, "poll_updated", "poll_event", e, map[string]interface{}{}); err != nil {
				return 0, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE app_users SET verified_subject = $2, verified_at = now() WHERE id = $1`, userID, subject); err != nil {
		return 0, fmt.Errorf("attach passport to %d: %w", userID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("attach verification: %w", err)
	}
	return from, nil
}
