package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is a signed-in account: one identity of one account provider.
type User struct {
	ID          int64  `json:"id"`
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
}

// Store is what the accounts keep: the users, their sessions and the
// one-time challenges of the providers. It owns the tables app_users,
// user_sessions and auth_challenges.
type Store interface {
	UpsertUser(ctx context.Context, provider, subject, displayName string) (*User, error)
	CreateSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error
	SessionUser(ctx context.Context, tokenHash []byte) (*User, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	CreateChallenge(ctx context.Context, provider, challenge string, expiresAt time.Time) error
	ConsumeChallenge(ctx context.Context, provider, challenge string) (bool, error)
	SetDisplayName(ctx context.Context, userID int64, name string) error
	DeleteUser(ctx context.Context, userID int64) error
	PurgeUsers(ctx context.Context, provider string) (int64, error)
}

// PGStore is the Postgres Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore keeps the accounts in the pool's database.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// userColumns read a User of the alias u.
const userColumns = `u.id, u.provider, u.display_name`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Provider, &u.DisplayName); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpsertUser returns the user of the identity, created on first sign-in.
func (s *PGStore) UpsertUser(ctx context.Context, provider, subject, displayName string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO app_users AS u (provider, subject, display_name) VALUES ($1, $2, $3)
		ON CONFLICT (provider, subject) DO UPDATE SET last_seen_at = now()
		RETURNING `+userColumns, provider, subject, displayName))
	if err != nil {
		return nil, fmt.Errorf("upsert app user: %w", err)
	}
	return u, nil
}

// CreateSession stores a session of the user by its token hash.
func (s *PGStore) CreateSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt); err != nil {
		return fmt.Errorf("create user session: %w", err)
	}
	return nil
}

// SessionUser returns the user of a live session, or nil.
func (s *PGStore) SessionUser(ctx context.Context, tokenHash []byte) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		SELECT `+userColumns+`
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

// DeleteSession ends a session; expired sessions are dropped on the way.
func (s *PGStore) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1 OR expires_at <= now()`, tokenHash); err != nil {
		return fmt.Errorf("delete user session: %w", err)
	}
	return nil
}

// CreateChallenge stores a one-time challenge of the provider; expired
// challenges are dropped on the way.
func (s *PGStore) CreateChallenge(ctx context.Context, provider, challenge string, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE expires_at <= now()`); err != nil {
		return fmt.Errorf("drop expired auth challenges: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_challenges (challenge, provider, expires_at) VALUES ($1, $2, $3)`,
		challenge, provider, expiresAt); err != nil {
		return fmt.Errorf("create auth challenge: %w", err)
	}
	return nil
}

// ConsumeChallenge deletes the challenge and tells whether it was a live
// challenge of the provider: a challenge serves once.
func (s *PGStore) ConsumeChallenge(ctx context.Context, provider, challenge string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_challenges WHERE challenge = $1 AND provider = $2 AND expires_at > now()`,
		challenge, provider)
	if err != nil {
		return false, fmt.Errorf("consume auth challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SetDisplayName sets the pseudonym the user chose: shown to them alone.
func (s *PGStore) SetDisplayName(ctx context.Context, userID int64, name string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE app_users SET display_name = $2 WHERE id = $1`, userID, name); err != nil {
		return fmt.Errorf("set display name of %d: %w", userID, err)
	}
	return nil
}

// DeleteUser erases the user with their sessions and all that references
// the account (the right to erasure).
func (s *PGStore) DeleteUser(ctx context.Context, userID int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_users WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("delete app user %d: %w", userID, err)
	}
	return nil
}

// PurgeUsers deletes the users of a provider with all that is theirs (for
// instance the noop test users before a launch).
func (s *PGStore) PurgeUsers(ctx context.Context, provider string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM app_users WHERE provider = $1`, provider)
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	return tag.RowsAffected(), nil
}
