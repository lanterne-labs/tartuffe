package polls_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/polls"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/sumdb/tlog"
)

// TestLogTreeProofs: every head proves each entry it holds and that it
// extends every head before it; a signed checkpoint opens with the
// verifier key only; the tree of entries logged before it is filled again.
func TestLogTreeProofs(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	var heads []polls.LogTree
	for i := 1; i <= 7; i++ {
		_, err := s.polls.Register(ctx, fmt.Sprintf("doc-%d", i), "noop", fmt.Sprint(1000+i))
		require.NoError(t, err)
		head, err := s.polls.Head(ctx)
		require.NoError(t, err)
		heads = append(heads, head)
	}
	last := heads[len(heads)-1]
	entries, err := s.polls.Log(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, entries, int(last.N))
	requireTree(t, s.polls, entries)

	for seq := int64(1); seq <= last.N; seq++ {
		p, err := s.polls.ProveEntry(ctx, last.N, seq)
		require.NoError(t, err)
		e := entries[seq-1]
		leaf := tlog.RecordHash(polls.LeafData(e.Seq, e.Kind, []byte(e.Body)))
		require.NoError(t, tlog.CheckRecord(p, last.N, last.Hash, seq-1, leaf), seq)
		assert.Error(t, tlog.CheckRecord(p, last.N, last.Hash, seq-1, tlog.RecordHash([]byte("forged"))))
	}
	for _, old := range heads[:len(heads)-1] {
		p, err := s.polls.ProveAppend(ctx, last.N, old.N)
		require.NoError(t, err)
		require.NoError(t, tlog.CheckTree(p, last.N, last.Hash, old.N, old.Hash), old.N)
	}
	_, err = s.polls.ProveEntry(ctx, last.N+1, 1)
	assert.ErrorIs(t, err, polls.ErrBadProofRequest, "a tree the log never had")

	skey, vkey, err := polls.GenerateLogKey()
	require.NoError(t, err)
	signer, err := polls.NewLogSigner(skey)
	require.NoError(t, err)
	assert.Equal(t, vkey, signer.VerifierKey(), "derived from the signer key")
	msg, err := signer.Sign(last)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(msg), polls.CheckpointText(last)))
	got, err := polls.OpenCheckpoint(msg, vkey)
	require.NoError(t, err)
	assert.Equal(t, last, got)
	_, otherKey, err := polls.GenerateLogKey()
	require.NoError(t, err)
	_, err = polls.OpenCheckpoint(msg, otherKey)
	assert.Error(t, err)
	_, err = polls.OpenCheckpoint([]byte(strings.Replace(string(msg), fmt.Sprintf("\n%d\n", last.N), fmt.Sprintf("\n%d\n", last.N+1), 1)), vkey)
	assert.Error(t, err, "a changed checkpoint")

	_, err = s.storer.Pool().Exec(ctx, `DELETE FROM vote_log_hashes WHERE idx >= $1`, tlog.StoredHashIndex(0, 3))
	require.NoError(t, err)
	require.NoError(t, s.polls.FillTree(ctx))
	head, err := s.polls.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, last, head)
}

// fakeRelay serves the events to every request, then EOSE, and records the
// filters it was asked.
type fakeRelay struct {
	mu      sync.Mutex
	events  []map[string]any
	filters []map[string]any
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := r.Context()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return
	}
	var req []json.RawMessage
	json.Unmarshal(raw, &req)
	var filter map[string]any
	json.Unmarshal(req[2], &filter)
	f.mu.Lock()
	f.filters = append(f.filters, filter)
	events := f.events
	f.mu.Unlock()
	for _, ev := range events {
		msg, _ := json.Marshal([]any{"EVENT", "ballots", ev})
		c.Write(ctx, websocket.MessageText, msg)
	}
	c.Write(ctx, websocket.MessageText, []byte(`["EOSE","ballots"]`))
	c.Read(ctx)
}

// TestReadRelays: the ballots of the relays' events are cast like posted
// ones, once though two relays hold them; an event without a valid ballot
// is dropped; the next read asks only the newer events.
func TestReadRelays(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	now := time.Now()
	e, cands := s.pollWith(t, "relay-poll", "relay candidate a", "relay candidate b")
	w := polls.OpenWindows(now)[0]
	_, err := s.polls.Register(ctx, "doc-r1", "noop", commitment(t, "r1"))
	require.NoError(t, err)
	b := ballot(t, e, w, 1, polls.KindChoices, []int64{cands[1], cands[0]}, "r1", nil)
	content, err := json.Marshal(b)
	require.NoError(t, err)
	hour := now.Truncate(time.Hour).Unix()
	relay := &fakeRelay{events: []map[string]any{
		{"id": "a", "kind": polls.NostrKind, "created_at": hour, "tags": [][]string{{"t", polls.NostrTag}}, "content": string(content)},
		{"id": "b", "kind": polls.NostrKind, "created_at": hour, "tags": [][]string{{"t", polls.NostrTag}}, "content": `{"poll":1,"kind":"choices"}`},
		{"id": "c", "kind": polls.NostrKind, "created_at": hour, "content": "not json"},
	}}
	one, two := httptest.NewServer(relay), httptest.NewServer(relay)
	defer one.Close()
	defer two.Close()
	urls := []string{"ws" + strings.TrimPrefix(one.URL, "http"), "ws" + strings.TrimPrefix(two.URL, "http")}
	svc := polls.NewService(s.polls, s.polls, nil, nil, polls.Config{Batches: polls.DefaultBatches, Relays: urls})

	svc.ReadRelays(ctx)
	published, err := s.polls.PublishBatch(ctx, 1, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.ID}, published)
	ballots, err := s.polls.Ballots(ctx, e.ID, w)
	require.NoError(t, err)
	require.Len(t, ballots, 1, "one ballot, though both relays hold it")
	assert.Equal(t, []int64{cands[1], cands[0]}, ballots[0].Choices)
	assert.Positive(t, ballots[0].LogSeq)

	svc.ReadRelays(ctx)
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.Len(t, relay.filters, 4)
	assert.Nil(t, relay.filters[0]["since"], "the first read asks everything")
	assert.Equal(t, float64(hour-2*3600), relay.filters[2]["since"], "the next one from the last event, with the slack")
}
