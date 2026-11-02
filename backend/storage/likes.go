package storage

import (
	"context"
	"errors"
	"fmt"
)

// ErrLikeTarget is returned when the liked fact or contradiction does not
// exist.
var ErrLikeTarget = errors.New("no such fact or contradiction")

// SetLike records (on) or removes the user's +1 on a fact or a contradiction,
// and returns the target's count of likes. A second +1 or a removal of a
// missing one changes nothing.
func (s *PostgresStorer) SetLike(ctx context.Context, userID int64, kind string, targetID int64, on bool) (int, error) {
	if on {
		var exists bool
		var err error
		switch kind {
		case FeedContradiction:
			err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contradictions WHERE id = $1)`, targetID).Scan(&exists)
		case FeedFact:
			err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM facts WHERE id = $1)`, targetID).Scan(&exists)
		default:
			return 0, ErrLikeTarget
		}
		if err != nil {
			return 0, fmt.Errorf("find like target: %w", err)
		}
		if !exists {
			return 0, ErrLikeTarget
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO feed_likes (user_id, target_kind, target_id) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, userID, kind, targetID); err != nil {
			return 0, fmt.Errorf("insert like: %w", err)
		}
	} else if _, err := s.pool.Exec(ctx, `
		DELETE FROM feed_likes WHERE user_id = $1 AND target_kind = $2 AND target_id = $3`, userID, kind, targetID); err != nil {
		return 0, fmt.Errorf("delete like: %w", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM feed_likes WHERE target_kind = $1 AND target_id = $2`, kind, targetID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count likes: %w", err)
	}
	return n, nil
}
