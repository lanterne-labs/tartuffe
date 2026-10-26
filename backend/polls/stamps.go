package polls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// OpenTimestamps anchors the checkpoints on Bitcoin for free: the public
// calendars put the checkpoint's digest in a Bitcoin transaction within
// hours. A checkpoint so stamped existed at that time; with two checkpoints
// of one size stamped, the log showed two histories.

// otsCalendars are the public calendars a checkpoint is sent to.
var otsCalendars = []string{
	"https://a.pool.opentimestamps.org",
	"https://b.pool.opentimestamps.org",
	"https://a.pool.eternitywall.com",
}

// otsHeader starts every .ots file: the magic bytes, version 1, and the
// SHA-256 file hash op (the digest follows).
var otsHeader = append([]byte("\x00OpenTimestamps\x00\x00Proof\x00\xbf\x89\xe2\xe8\x84\xe8\x92\x94"), 0x01, 0x08)

// otsFork prefixes each branch of a timestamp but the last.
const otsFork = 0xff

// Stamp is a checkpoint and its OpenTimestamps proof.
type Stamp struct {
	Size       int64     `json:"size"`
	Checkpoint string    `json:"checkpoint"`
	OTS        []byte    `json:"ots"`
	CreatedAt  time.Time `json:"created_at"`
}

// stampClient is the client of the calendars (an API, not a site we read).
var stampClient = &http.Client{Timeout: 20 * time.Second}

// otsFile sends the digest to the calendars and returns the .ots file of
// the ones that answered (pending attestations: `ots upgrade` completes them
// once Bitcoin confirms).
func otsFile(ctx context.Context, digest [32]byte) ([]byte, error) {
	var branches [][]byte
	var errs []error
	for _, cal := range otsCalendars {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cal+"/digest", bytes.NewReader(digest[:]))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.opentimestamps.v1")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := stampClient.Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cal, err))
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10_000))
		resp.Body.Close()
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", cal, err))
		case resp.StatusCode != http.StatusOK:
			errs = append(errs, fmt.Errorf("%s: HTTP %d", cal, resp.StatusCode))
		case len(body) == 0 || body[0] == otsFork:
			// A timestamp of several branches cannot be joined to the
			// others by a fork byte; calendars answer with one path.
			errs = append(errs, fmt.Errorf("%s: an answer of several branches", cal))
		default:
			branches = append(branches, body)
		}
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("no OpenTimestamps calendar answered: %w", errors.Join(errs...))
	}
	if len(errs) > 0 {
		log.Warn().Err(errors.Join(errs...)).Int("answered", len(branches)).Msg("OpenTimestamps calendars")
	}
	out := append(append([]byte{}, otsHeader...), digest[:]...)
	for i, b := range branches {
		if i < len(branches)-1 {
			out = append(out, otsFork)
		}
		out = append(out, b...)
	}
	return out, nil
}

// StampHead stamps the signed checkpoint of the log's head, unless it was
// stamped already (or the log is empty).
func (s *PGStore) StampHead(ctx context.Context, signer *LogSigner) (*Stamp, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return nil, err
	}
	if head.N == 0 {
		return nil, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote_log_stamps WHERE size = $1)`, head.N).Scan(&exists); err != nil {
		return nil, fmt.Errorf("vote log stamps: %w", err)
	}
	if exists {
		return nil, nil
	}
	msg, err := signer.Sign(head)
	if err != nil {
		return nil, err
	}
	ots, err := otsFile(ctx, CheckpointDigest(msg))
	if err != nil {
		return nil, err
	}
	st := &Stamp{Size: head.N, Checkpoint: string(msg), OTS: ots}
	if err := s.pool.QueryRow(ctx, `INSERT INTO vote_log_stamps (size, checkpoint, ots) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING RETURNING created_at`,
		st.Size, st.Checkpoint, st.OTS).Scan(&st.CreatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("save vote log stamp: %w", err)
	}
	return st, nil
}

// Stamps returns the stamped checkpoints, oldest first.
func (s *PGStore) Stamps(ctx context.Context) ([]Stamp, error) {
	rows, err := s.pool.Query(ctx, `SELECT size, checkpoint, ots, created_at FROM vote_log_stamps ORDER BY size`)
	if err != nil {
		return nil, fmt.Errorf("vote log stamps: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Stamp, error) {
		var st Stamp
		err := r.Scan(&st.Size, &st.Checkpoint, &st.OTS, &st.CreatedAt)
		return st, err
	})
	if err != nil {
		return nil, fmt.Errorf("vote log stamps: %w", err)
	}
	return out, nil
}

// StampNote stamps a signed checkpoint note of the given tree size with
// OpenTimestamps, once per size (the vote protocol's checkpoints carry
// extension lines, so they are stamped as they are served).
func (s *PGStore) StampNote(ctx context.Context, size int64, msg []byte) (*Stamp, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote_log_stamps WHERE size = $1)`, size).Scan(&exists); err != nil {
		return nil, fmt.Errorf("vote log stamps: %w", err)
	}
	if exists {
		return nil, nil
	}
	ots, err := otsFile(ctx, CheckpointDigest(msg))
	if err != nil {
		return nil, err
	}
	st := &Stamp{Size: size, Checkpoint: string(msg), OTS: ots}
	if err := s.pool.QueryRow(ctx, `INSERT INTO vote_log_stamps (size, checkpoint, ots) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING RETURNING created_at`,
		st.Size, st.Checkpoint, st.OTS).Scan(&st.CreatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("save vote log stamp: %w", err)
	}
	return st, nil
}
