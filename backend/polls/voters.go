package polls

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"digital-mirror/backend/polls/semaphore"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The outcomes of a registration.
const (
	// RegNew: the document's first commitment, a voter of the open windows
	// at once.
	RegNew = "registered"
	// RegReplaced: the document had another commitment; the new one votes
	// from the next window of each series, the old one keeps the open ones.
	RegReplaced = "replaced"
	// RegSame: the document already holds this commitment.
	RegSame = "already"
)

// Registration is what a registration did: Status, and the windows from
// which the commitment votes.
type Registration struct {
	Status string    `json:"status"`
	From   [2]Window `json:"from"`
}

// ErrCommitmentTaken refuses a commitment another document already admitted.
var ErrCommitmentTaken = errors.New("this voting key is already registered by another document")

// Register admits the commitment for the document (an HMAC of its
// identifier) and logs it. A document holds one commitment per window: a new
// one replaces the old from the next window of each series.
func (s *PGStore) Register(ctx context.Context, document, provider, commitment string) (Registration, error) {
	c, err := semaphore.ParseField(commitment)
	if err != nil || c.Sign() == 0 {
		return Registration{}, fmt.Errorf("%w: the commitment is not a field element", ErrBallot)
	}
	commitment = c.String()
	now := s.now()
	open := OpenWindows(now)

	s.treesMu.Lock()
	defer s.treesMu.Unlock()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Registration{}, fmt.Errorf("register voter: %w", err)
	}
	defer tx.Rollback(ctx)
	// A tree changed in memory but not committed is dropped, to be built
	// again from the table.
	mutated, committed := false, false
	defer func() {
		if mutated && !committed {
			clear(s.trees)
		}
	}()
	// The open windows' trees, before the new key is in the table: built
	// after, they would hold it already.
	trees := make(map[Window]*semaphore.Tree, len(open))
	for _, w := range open {
		if trees[w], err = s.tree(ctx, tx, w); err != nil {
			return Registration{}, err
		}
	}

	var (
		oldID     int64
		oldCommit string
		oldFrom   [2]int
		hasOld    bool
	)
	err = tx.QueryRow(ctx, `SELECT id, commitment, from_a, from_b FROM voter_keys WHERE document = $1 AND until_a IS NULL FOR UPDATE`, document).
		Scan(&oldID, &oldCommit, &oldFrom[0], &oldFrom[1])
	switch {
	case err == nil:
		hasOld = true
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return Registration{}, fmt.Errorf("voter key of document: %w", err)
	}

	reg := Registration{Status: RegNew, From: open}
	entry := registerEntry{Document: document, Provider: provider, Commitment: commitment}
	if hasOld {
		if oldCommit == commitment {
			return Registration{Status: RegSame, From: [2]Window{{0, oldFrom[0]}, {1, oldFrom[1]}}}, nil
		}
		reg.Status = RegReplaced
		reg.From = [2]Window{{0, open[0].Index + 1}, {1, open[1].Index + 1}}
		entry.Replaces = oldCommit
		if oldFrom[0] > open[0].Index {
			// The old commitment was itself waiting for the next
			// window: it never voted, the new one takes its place.
			if _, err := tx.Exec(ctx, `DELETE FROM voter_keys WHERE id = $1`, oldID); err != nil {
				return Registration{}, fmt.Errorf("drop waiting voter key: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `UPDATE voter_keys SET until_a = $2, until_b = $3 WHERE id = $1`,
			oldID, reg.From[0].Index, reg.From[1].Index); err != nil {
			return Registration{}, fmt.Errorf("close voter key: %w", err)
		}
	}
	entry.From = [2]int{reg.From[0].Index, reg.From[1].Index}
	if _, err := tx.Exec(ctx, `INSERT INTO voter_keys (document, provider, commitment, from_a, from_b) VALUES ($1, $2, $3, $4, $5)`,
		document, provider, commitment, entry.From[0], entry.From[1]); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Registration{}, ErrCommitmentTaken
		}
		return Registration{}, fmt.Errorf("insert voter key: %w", err)
	}
	if _, err := appendLog(ctx, tx, LogRegister, entry); err != nil {
		return Registration{}, err
	}
	// A new voter joins the open windows' trees now; a replaced one joins
	// the next windows' trees when they open (they are built then).
	if reg.Status == RegNew {
		for _, w := range open {
			t := trees[w]
			mutated = true
			if err := t.Insert(c); err != nil {
				return Registration{}, err
			}
			if err := recordRoot(ctx, tx, w, t); err != nil {
				return Registration{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Registration{}, fmt.Errorf("register voter: %w", err)
	}
	committed = true
	return reg, nil
}

// tree returns the voter tree of the window, built from the voter keys on
// first use (the caller holds treesMu). Only the open windows are kept.
func (s *PGStore) tree(ctx context.Context, q pgx.Tx, w Window) (*semaphore.Tree, error) {
	if t, ok := s.trees[w]; ok {
		return t, nil
	}
	from, until := "from_a", "until_a"
	if w.Series == 1 {
		from, until = "from_b", "until_b"
	}
	rows, err := q.Query(ctx, `SELECT commitment FROM voter_keys WHERE `+from+` <= $1 AND (`+until+` IS NULL OR $1 < `+until+`) ORDER BY id`, w.Index)
	if err != nil {
		return nil, fmt.Errorf("voter keys of window %v: %w", w, err)
	}
	commitments, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("voter keys of window %v: %w", w, err)
	}
	leaves := make([]*big.Int, len(commitments))
	for i, c := range commitments {
		leaves[i], _ = new(big.Int).SetString(c, 10)
	}
	t, err := semaphore.NewTree(leaves)
	if err != nil {
		return nil, fmt.Errorf("voter tree of window %v: %w", w, err)
	}
	if t.Size() > 0 {
		if err := recordRoot(ctx, q, w, t); err != nil {
			return nil, err
		}
	}
	open := OpenWindows(s.now())
	for k := range s.trees {
		if k != open[0] && k != open[1] {
			delete(s.trees, k)
		}
	}
	s.trees[w] = t
	return t, nil
}

// recordRoot keeps the tree's root as one a ballot of the window may prove
// against, and logs it the first time.
func recordRoot(ctx context.Context, tx pgx.Tx, w Window, t *semaphore.Tree) error {
	root := t.Root().String()
	tag, err := tx.Exec(ctx, `INSERT INTO voter_roots (series, win, root, size) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		w.Series, w.Index, root, t.Size())
	if err != nil {
		return fmt.Errorf("record voter root: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = appendLog(ctx, tx, LogRoot, rootEntry{Window: w, Root: root, Size: t.Size()})
	return err
}

// VoterTree is a window's tree as the browser rebuilds it: every
// commitment, in order. A voter downloads all of them, never their own path,
// so asking says nothing of which one is theirs.
type VoterTree struct {
	Window
	Root   string   `json:"root"`
	Leaves []string `json:"leaves"`
}

// ErrWindowNotOpen refuses a window that is not open now.
var ErrWindowNotOpen = errors.New("this window is not open")

// Tree returns the tree of an open window.
func (s *PGStore) Tree(ctx context.Context, w Window) (*VoterTree, error) {
	open := OpenWindows(s.now())
	if w != open[0] && w != open[1] {
		return nil, ErrWindowNotOpen
	}
	s.treesMu.Lock()
	defer s.treesMu.Unlock()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("voter tree: %w", err)
	}
	defer tx.Rollback(ctx)
	t, err := s.tree(ctx, tx, w)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		delete(s.trees, w)
		return nil, fmt.Errorf("voter tree: %w", err)
	}
	out := &VoterTree{Window: w, Root: t.Root().String(), Leaves: make([]string, t.Size())}
	for i, l := range t.Leaves() {
		out.Leaves[i] = l.String()
	}
	return out, nil
}

// knownRoot tells whether the window's tree had the root.
func knownRoot(ctx context.Context, q pgx.Tx, w Window, root string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM voter_roots WHERE series = $1 AND win = $2 AND root = $3)`, w.Series, w.Index, root).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("voter root: %w", err)
	}
	return ok, nil
}

// ResetVotes deletes every voting key, root, ballot and log entry: before a
// launch, when the test ballots (which nothing tells apart) must go.
func (s *PGStore) ResetVotes(ctx context.Context) error {
	s.treesMu.Lock()
	defer s.treesMu.Unlock()
	if _, err := s.pool.Exec(ctx, `TRUNCATE anon_ballots, voter_roots, voter_keys, vote_log, vote_log_hashes, vote_log_stamps, nostr_cursors, device_links RESTART IDENTITY`); err != nil {
		return fmt.Errorf("reset votes: %w", err)
	}
	clear(s.trees)
	return nil
}
