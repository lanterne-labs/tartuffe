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
// Verified says a verified identity (a document) is attached: the account
// may vote.
type User struct {
	ID          int64  `json:"id"`
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	Verified    bool   `json:"verified"`
}

// ErrOtherDocument is returned when an account that holds a verified
// identity is shown another document.
var ErrOtherDocument = errors.New("the account holds another document")

// Store is what the accounts keep: the users, their sessions, the one-time
// challenges of the providers and the verified identities. It owns the
// tables app_users, user_sessions and auth_challenges.
type Store interface {
	UpsertUser(ctx context.Context, provider, subject, displayName string) (*User, error)
	CreateSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error
	SessionUser(ctx context.Context, tokenHash []byte) (*User, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	CreateChallenge(ctx context.Context, provider, challenge string, expiresAt time.Time) error
	ConsumeChallenge(ctx context.Context, provider, challenge string) (bool, error)
	AttachVerification(ctx context.Context, userID int64, subject string) (int64, error)
	DeleteUser(ctx context.Context, userID int64) error
	PurgeUsers(ctx context.Context, provider string) (int64, error)
}

// Hooks let the other parts of the site follow an account without the
// accounts reading their tables. Each runs inside the accounts' transaction:
// an error rolls the change back.
type Hooks struct {
	// Moved runs when a verified identity moves from one account to another:
	// what the identity carries (the ballots) follows it.
	Moved []MovedHook
	// Deleting runs before an account is deleted (its rows that reference
	// app_users go with it).
	Deleting []DeletingHook
}

// MovedHook follows a verified identity from one account to another.
type MovedHook func(ctx context.Context, tx pgx.Tx, from, to int64) error

// DeletingHook runs before an account is deleted.
type DeletingHook func(ctx context.Context, tx pgx.Tx, userID int64) error

// PGStore is the Postgres Store.
type PGStore struct {
	pool  *pgxpool.Pool
	hooks Hooks
}

// NewPGStore keeps the accounts in the pool's database.
func NewPGStore(pool *pgxpool.Pool, hooks Hooks) *PGStore {
	return &PGStore{pool: pool, hooks: hooks}
}

// userColumns read a User of the alias u.
const userColumns = `u.id, u.provider, u.display_name, u.verified_subject IS NOT NULL`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Provider, &u.DisplayName, &u.Verified); err != nil {
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

// AttachVerification gives the account a verified identity: the subject
// "<provider>:<id>" names one document (a passport for zkpassport), which
// gives the right to vote. When another account holds it, the identity
// moves: the Moved hooks carry what it holds (the ballots) to this account,
// and the other account loses the right to vote. It returns the account it
// moved from (0: none). Showing the account's own document again changes
// nothing; another document is ErrOtherDocument.
func (s *PGStore) AttachVerification(ctx context.Context, userID int64, subject string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("attach verification: %w", err)
	}
	defer tx.Rollback(ctx)

	// The holder of the identity, if any, and this account, locked in the
	// order of their ids so two moves never wait on each other.
	var from int64
	err = tx.QueryRow(ctx, `SELECT id FROM app_users WHERE verified_subject = $1`, subject).Scan(&from)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find identity holder: %w", err)
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
		return 0, fmt.Errorf("find identity holder: %w", err)
	}
	if from != 0 {
		if _, err := tx.Exec(ctx, `UPDATE app_users SET verified_subject = NULL, verified_at = NULL WHERE id = $1`, from); err != nil {
			return 0, fmt.Errorf("detach identity from %d: %w", from, err)
		}
		for _, h := range s.hooks.Moved {
			if err := h(ctx, tx, from, userID); err != nil {
				return 0, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE app_users SET verified_subject = $2, verified_at = now() WHERE id = $1`, userID, subject); err != nil {
		return 0, fmt.Errorf("attach identity to %d: %w", userID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("attach verification: %w", err)
	}
	return from, nil
}

// DeleteUser erases the user with their sessions and all that references
// the account (the right to erasure), after the Deleting hooks.
func (s *PGStore) DeleteUser(ctx context.Context, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete app user: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, h := range s.hooks.Deleting {
		if err := h(ctx, tx, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_users WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("delete app user %d: %w", userID, err)
	}
	return tx.Commit(ctx)
}

// PurgeUsers deletes the users of a provider with all that is theirs (for
// instance the noop test users before a launch).
func (s *PGStore) PurgeUsers(ctx context.Context, provider string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM app_users WHERE provider = $1`, provider)
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	for _, id := range ids {
		for _, h := range s.hooks.Deleting {
			if err := h(ctx, tx, id); err != nil {
				return 0, err
			}
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM app_users WHERE provider = $1`, provider)
	if err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("purge app users of %s: %w", provider, err)
	}
	return tag.RowsAffected(), nil
}

// LockVerified locks the account in tx and tells whether it holds a verified
// identity. The polls call it in their ballot's transaction, so a document
// moving to another account (AttachVerification locks the same rows) never
// leaves a vote behind on the old one.
func LockVerified(ctx context.Context, tx pgx.Tx, userID int64) (bool, error) {
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT verified_subject IS NOT NULL FROM app_users WHERE id = $1 FOR UPDATE`, userID).Scan(&verified); err != nil {
		return false, fmt.Errorf("lock app user %d: %w", userID, err)
	}
	return verified, nil
}
