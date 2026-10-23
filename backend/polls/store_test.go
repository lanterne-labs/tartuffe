package polls_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/polls/semaphore"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stores are the polls on the test database, with a clean slate of polls,
// voters and ballots.
type stores struct {
	storer *storage.PostgresStorer
	polls  *polls.PGStore
}

func openStores(t *testing.T) stores {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping database tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	_, err = storer.Pool().Exec(ctx, "TRUNCATE app_users, poll_events, auth_challenges, voter_keys, voter_roots, vote_log, vote_log_hashes, vote_log_stamps, nostr_cursors, key_vault, device_links RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	return stores{storer: storer, polls: polls.NewPGStore(storer.Pool())}
}

// pollWith makes an open poll with candidates, and returns it and them.
func (s stores) pollWith(t *testing.T, slug string, names ...string) (*polls.Event, []int64) {
	t.Helper()
	ctx := context.Background()
	id, err := s.polls.SaveEvent(ctx, polls.Event{Slug: slug, TitleFR: "Test", TitleEN: "Test",
		MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-100 * 24 * time.Hour), Featured: true})
	require.NoError(t, err)
	var ids []int64
	for _, n := range names {
		p, err := s.storer.FindOrInsertPersonWithType(ctx, n, "human")
		require.NoError(t, err)
		_, err = s.polls.AddCandidate(ctx, id, p)
		require.NoError(t, err)
		ids = append(ids, p)
	}
	e, err := s.polls.GetEvent(ctx, slug)
	require.NoError(t, err)
	return e, ids
}

// proved is a proof made by the browser's libraries (testdata/prove.mjs).
type proved struct {
	Proof      semaphore.Proof `json:"proof"`
	Commitment string          `json:"commitment"`
	Root       string          `json:"root"`
}

// prove makes the proof of the identity of secret, in the group of others
// then itself, for the message and scope. It skips the test when Node or the
// frontend's libraries are missing.
func prove(t *testing.T, secret string, others []string, message, scope string) proved {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	if _, err := os.Stat("../../frontend/node_modules/@semaphore-protocol/proof"); err != nil {
		t.Skip("run npm ci in frontend/ for the Semaphore libraries")
	}
	in, _ := json.Marshal(map[string]any{"secret": secret, "others": others, "message": message, "scope": scope})
	cmd := exec.Command("node", "testdata/prove.mjs")
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	var p proved
	require.NoError(t, json.Unmarshal(out.Bytes(), &p))
	return p
}

// commitment is the commitment of the identity of secret.
func commitment(t *testing.T, secret string) string {
	return prove(t, secret, nil, "1", "1").Commitment
}

// ballot builds a ballot of the event in the window (the ranked choices) and
// proves it as secret, a member after others.
func ballot(t *testing.T, e *polls.Event, w polls.Window, seq int, kind string, choices []int64, secret string, others []string) polls.AnonBallot {
	t.Helper()
	b := polls.AnonBallot{EventID: e.ID, Window: w, Seq: seq, Kind: kind, Choices: choices}
	p := prove(t, secret, others, semaphore.Digest([]byte(b.Message())).String(), polls.Scope(e.ID, w))
	b.Proof = p.Proof
	return b
}

// TestRegisterAndTrees: a document admits one commitment; a new one replaces
// it at the next window of each series, where the old one leaves the tree;
// the trees' roots match the browser's.
func TestRegisterAndTrees(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.polls.SetClock(func() time.Time { return now })
	open := polls.OpenWindows(now)

	cb, ca := commitment(t, "b"), commitment(t, "a")
	reg, err := s.polls.Register(ctx, "doc-b", "noop", cb)
	require.NoError(t, err)
	assert.Equal(t, polls.RegNew, reg.Status)
	assert.Equal(t, open, reg.From)
	reg, err = s.polls.Register(ctx, "doc-a", "noop", ca)
	require.NoError(t, err)
	assert.Equal(t, polls.RegNew, reg.Status)

	again, err := s.polls.Register(ctx, "doc-a", "noop", ca)
	require.NoError(t, err)
	assert.Equal(t, polls.RegSame, again.Status)
	_, err = s.polls.Register(ctx, "doc-c", "noop", ca)
	assert.ErrorIs(t, err, polls.ErrCommitmentTaken)

	tree, err := s.polls.Tree(ctx, open[0])
	require.NoError(t, err)
	assert.Equal(t, []string{cb, ca}, tree.Leaves)
	assert.Equal(t, prove(t, "a", []string{"b"}, "1", "1").Root, tree.Root, "the root the browser computes")

	// doc-a lost its key: the new one waits for the next windows.
	ca2 := commitment(t, "a2")
	reg, err = s.polls.Register(ctx, "doc-a", "noop", ca2)
	require.NoError(t, err)
	assert.Equal(t, polls.RegReplaced, reg.Status)
	assert.Equal(t, open[0].Index+1, reg.From[0].Index)
	assert.Equal(t, open[1].Index+1, reg.From[1].Index)
	tree, err = s.polls.Tree(ctx, open[1])
	require.NoError(t, err)
	assert.Equal(t, []string{cb, ca}, tree.Leaves, "the open windows keep the old key")

	// Replaced again before the next window: the waiting key is dropped.
	ca3 := commitment(t, "a3")
	_, err = s.polls.Register(ctx, "doc-a", "noop", ca3)
	require.NoError(t, err)

	_, err = s.polls.Tree(ctx, polls.Window{Series: 0, Index: open[0].Index + 1})
	assert.ErrorIs(t, err, polls.ErrWindowNotOpen)

	// The next window of series 0 opens: the old key is out, the last new
	// one in.
	later := open[0].End().Add(time.Minute)
	s.polls.SetClock(func() time.Time { return later })
	tree, err = s.polls.Tree(ctx, polls.WindowAt(0, later))
	require.NoError(t, err)
	assert.Equal(t, []string{cb, ca3}, tree.Leaves)
	if polls.WindowAt(1, later) == open[1] {
		tree, err = s.polls.Tree(ctx, open[1])
		require.NoError(t, err)
		assert.Equal(t, []string{cb, ca}, tree.Leaves, "series 1's window is still the old one")
	}

	// Every registration and root is in the log, and the log chains.
	entries, err := s.polls.Log(ctx, 0, 0)
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.Kind]++
	}
	assert.Equal(t, 4, kinds[polls.LogRegister], "b, a, a2, a3; the repeat and the refusal log nothing")
	assert.Positive(t, kinds[polls.LogRoot])
	requireTree(t, s.polls, entries)
	assert.Empty(t, polls.Audit(entries).Problems, "the registrations and roots replay")
	tampered := append([]polls.LogEntry(nil), entries...)
	tampered[1].Body = `{"document":"forged"}`
	forged, err := polls.TreeOf(tampered)
	require.NoError(t, err)
	head, err := s.polls.Head(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, head.Hash, forged.Hash)
}

// requireTree checks that the entries (the whole log) make the log's tree.
func requireTree(t *testing.T, s *polls.PGStore, entries []polls.LogEntry) {
	t.Helper()
	head, err := s.Head(context.Background())
	require.NoError(t, err)
	tree, err := polls.TreeOf(entries)
	require.NoError(t, err)
	assert.Equal(t, head, tree)
}

// TestCastPublishTally: a ballot needs a known root and a higher seq; a
// batch waits for enough ballots, publishes them in the log, and the tally
// counts the last ballot of each voter, blank votes included, withdrawals
// not.
func TestCastPublishTally(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.polls.SetClock(func() time.Time { return now })
	e, cands := s.pollWith(t, "anon-poll", "anon candidate a", "anon candidate b")
	w := polls.ResultWindow(now)

	secrets := []string{"v1", "v2", "v3", "v4"}
	for i, sec := range secrets {
		_, err := s.polls.Register(ctx, "doc-"+sec, "noop", commitment(t, sec))
		require.NoError(t, err, i)
	}
	// Each voter proves against the tree as it was right after their
	// registration: a root the window had.
	others := func(sec string) []string {
		var out []string
		for _, o := range secrets {
			if o == sec {
				break
			}
			out = append(out, o)
		}
		return out
	}
	cast := func(sec string, seq int, kind string, choices ...int64) error {
		b := ballot(t, e, w, seq, kind, choices, sec, others(sec))
		require.NoError(t, polls.CheckAnonBallot(e, cands, b, now))
		return s.polls.Cast(ctx, b)
	}
	require.NoError(t, cast("v1", 1, polls.KindChoices, cands[0], cands[1]))
	require.NoError(t, cast("v2", 1, polls.KindBlank))
	require.NoError(t, cast("v3", 1, polls.KindChoices, cands[1]))
	require.NoError(t, cast("v3", 2, polls.KindChoices, cands[0]))
	var stale *polls.StaleSeqError
	require.ErrorAs(t, cast("v3", 2, polls.KindChoices, cands[1]), &stale)
	assert.Equal(t, 2, stale.Last)
	require.NoError(t, cast("v4", 1, polls.KindChoices, cands[1]))
	require.NoError(t, cast("v4", 2, polls.KindWithdraw))

	// A root the window never had.
	b := ballot(t, e, w, 1, polls.KindBlank, nil, "stranger", []string{"x"})
	assert.ErrorIs(t, s.polls.Cast(ctx, b), polls.ErrUnknownRoot)

	// Nothing is counted before a batch publishes it.
	_, turnout, err := s.polls.Tallies(ctx, e.ID)
	require.NoError(t, err)
	assert.Zero(t, turnout.Voters)

	events, err := s.polls.PublishBatch(ctx, 100, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, events, "too few ballots, none waited long")
	events, err = s.polls.PublishBatch(ctx, 3, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.ID}, events)

	tallies, turnout, err := s.polls.Tallies(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, polls.Turnout{Voters: 3, Blank: 1}, turnout)
	byID := map[int64]polls.CandidateTally{}
	for _, c := range tallies {
		byID[c.PersonID] = c
	}
	assert.Equal(t, 2, byID[cands[0]].FirstChoices, "v1, and v3's second ballot")
	assert.Equal(t, 0, byID[cands[1]].FirstChoices)
	assert.Equal(t, 1, byID[cands[1]].Ranked, "v1's; v4 withdrew")
	assert.Equal(t, 2, byID[cands[0]].Beats[cands[1]])
	assert.Equal(t, 0, byID[cands[1]].Beats[cands[0]])

	r := polls.Compute(e, tallies, turnout, now)
	require.NotNil(t, r.BlankShare)
	assert.InDelta(t, 1.0/3, *r.BlankShare, 1e-9)

	// A removed candidate: v1's second choice moves up; v3's ranked only the
	// removed one and no longer counts.
	_, err = s.polls.RemoveCandidate(ctx, e.ID, cands[0])
	require.NoError(t, err)
	tallies, _, err = s.polls.Tallies(ctx, e.ID)
	require.NoError(t, err)
	require.Len(t, tallies, 1)
	assert.Equal(t, 1, tallies[0].FirstChoices)
	_, turnout, err = s.polls.Tallies(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, polls.Turnout{Voters: 2, Blank: 1}, turnout, "v1 and the blank vote")

	ballots, err := s.polls.Ballots(ctx, e.ID, w)
	require.NoError(t, err)
	assert.Len(t, ballots, 4, "the last ballot of each nullifier, withdrawals included")

	entries, err := s.polls.Log(ctx, 0, 0)
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, en := range entries {
		kinds[en.Kind]++
	}
	assert.Equal(t, 6, kinds[polls.LogBallot])
	assert.Equal(t, 1, kinds[polls.LogTally])
	requireTree(t, s.polls, entries)

	// Anyone replays the log; a changed tally or ballot shows.
	report := polls.Audit(entries)
	assert.Empty(t, report.Problems)
	assert.Equal(t, 6, report.Ballots)
	changed := 0
	for i, en := range entries {
		forged := append([]polls.LogEntry(nil), entries...)
		switch en.Kind {
		case polls.LogTally:
			forged[i].Body = strings.Replace(en.Body, `"voters":3`, `"voters":4`, 1)
		case polls.LogBallot:
			forged[i].Body = strings.Replace(en.Body, `"kind":"blank"`, `"kind":"withdraw"`, 1)
		default:
			continue
		}
		if forged[i].Body != en.Body {
			changed++
			assert.NotEmpty(t, polls.Audit(forged).Problems, "entry %d changed", en.Seq)
		}
	}
	assert.Equal(t, 2, changed, "the tally and the blank ballot")
}

// TestCheckAnonBallot: the proof binds the ballot, the scope binds the poll
// and the window.
func TestCheckAnonBallot(t *testing.T) {
	s := openStores(t)
	now := time.Now()
	e, cands := s.pollWith(t, "check-poll", "check candidate a", "check candidate b")
	open := polls.OpenWindows(now)
	b := ballot(t, e, open[1], 1, polls.KindChoices, []int64{cands[0], cands[1]}, "c1", nil)
	require.NoError(t, polls.CheckAnonBallot(e, cands, b, now))

	changed := b
	changed.Choices = []int64{cands[1]}
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, changed, now), polls.ErrBallot, "another ballot under the proof")
	other := b
	other.Window = open[0]
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, other, now), polls.ErrBallot, "another scope")
	blankWithChoice := b
	blankWithChoice.Kind = polls.KindBlank
	reordered := b
	reordered.Choices = []int64{cands[1], cands[0]}
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, reordered, now), polls.ErrBallot, "the order is bound by the proof")
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, blankWithChoice, now), polls.ErrBallot, "blank is exclusive")
	empty := b
	empty.Choices = nil
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, empty, now), polls.ErrBallot, "no choice is not a blank vote")
	old := b
	old.Index--
	assert.ErrorIs(t, polls.CheckAnonBallot(e, cands, old, now), polls.ErrBallot, "a closed window")
}

func TestVaultAndLinks(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	lookup := hex.EncodeToString(bytes.Repeat([]byte{1}, 32))
	got, err := s.polls.VaultGet(ctx, lookup)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, s.polls.VaultPut(ctx, lookup, "c1", "token-one"))
	require.NoError(t, s.polls.VaultPut(ctx, lookup, "c2", "token-one"))
	assert.ErrorIs(t, s.polls.VaultPut(ctx, lookup, "c3", "token-two"), polls.ErrVaultWrite)
	got, err = s.polls.VaultGet(ctx, lookup)
	require.NoError(t, err)
	assert.Equal(t, "c2", got)

	channel := hex.EncodeToString(bytes.Repeat([]byte{2}, 32))
	got, err = s.polls.LinkTake(ctx, channel)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, s.polls.LinkPut(ctx, channel, "secret"))
	assert.ErrorIs(t, s.polls.LinkPut(ctx, channel, "other"), polls.ErrLinkUsed)
	got, err = s.polls.LinkTake(ctx, channel)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
	got, err = s.polls.LinkTake(ctx, channel)
	require.NoError(t, err)
	assert.Empty(t, got, "taken once")
}

// TestConcurrentRegistrations: registrations at once all land in the tree.
func TestConcurrentRegistrations(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := semaphore.Digest([]byte{byte(i)}).String()
			_, err := s.polls.Register(ctx, "doc-"+c, "noop", c)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	tree, err := s.polls.Tree(ctx, polls.OpenWindows(time.Now())[0])
	require.NoError(t, err)
	assert.Len(t, tree.Leaves, 8)
}
