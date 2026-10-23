package polls

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// The vault and the device links carry the voter's secret, encrypted on the
// device: the server keeps only ciphertext it cannot open, under names it
// cannot tie to a voter.

// vaultName is a lookup, a write token or a channel: 32 to 128 hex or
// base64url characters, chosen by the device.
var vaultName = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

// maxCiphertext bounds a vault entry or a link (a secret, a session token).
const maxCiphertext = 4096

// ErrVaultWrite refuses to replace a vault entry without its write token.
var ErrVaultWrite = errors.New("this vault entry belongs to another passkey")

// VaultGet returns the ciphertext under the lookup ("" when there is none).
func (s *PGStore) VaultGet(ctx context.Context, lookup string) (string, error) {
	var c string
	err := s.pool.QueryRow(ctx, `SELECT ciphertext FROM key_vault WHERE lookup = $1`, lookup).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read vault: %w", err)
	}
	return c, nil
}

// VaultPut keeps the ciphertext under the lookup. An entry is replaced only
// with the write token it was created with.
func (s *PGStore) VaultPut(ctx context.Context, lookup, ciphertext, writeToken string) error {
	h := sha256.Sum256([]byte(writeToken))
	hash := hex.EncodeToString(h[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("write vault: %w", err)
	}
	defer tx.Rollback(ctx)
	var stored string
	err = tx.QueryRow(ctx, `SELECT write_hash FROM key_vault WHERE lookup = $1 FOR UPDATE`, lookup).Scan(&stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `INSERT INTO key_vault (lookup, ciphertext, write_hash) VALUES ($1, $2, $3)`, lookup, ciphertext, hash); err != nil {
			return fmt.Errorf("write vault: %w", err)
		}
	case err != nil:
		return fmt.Errorf("write vault: %w", err)
	default:
		if subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
			return ErrVaultWrite
		}
		if _, err := tx.Exec(ctx, `UPDATE key_vault SET ciphertext = $2, updated_at = now() WHERE lookup = $1`, lookup, ciphertext); err != nil {
			return fmt.Errorf("write vault: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// LinkTTL is how long a device link waits for the new device.
const LinkTTL = 10 * time.Minute

// ErrLinkUsed refuses a second answer to one link code.
var ErrLinkUsed = errors.New("this link code was already answered: show a new one")

// LinkPut leaves the old device's answer to a link code.
func (s *PGStore) LinkPut(ctx context.Context, channel, ciphertext string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM device_links WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("purge device links: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO device_links (channel, ciphertext, expires_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		channel, ciphertext, s.now().Add(LinkTTL))
	if err != nil {
		return fmt.Errorf("answer device link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkUsed
	}
	return nil
}

// LinkTake returns the answer to a link code once, and forgets it ("" while
// there is none).
func (s *PGStore) LinkTake(ctx context.Context, channel string) (string, error) {
	var c string
	err := s.pool.QueryRow(ctx, `DELETE FROM device_links WHERE channel = $1 AND expires_at >= now() RETURNING ciphertext`, channel).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read device link: %w", err)
	}
	return c, nil
}
