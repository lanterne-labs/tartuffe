package vote

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/grumpkin"
)

// encryptedBallot builds a logged ballot body for a ranking (no proof: the
// tallier trusts the log, which the ingest verified).
func encryptedBallot(t *testing.T, pk ballot.PublicKeys, nullifier string, seq uint32, r ballot.Ranking) ([]byte, ballot.Ciphertext) {
	t.Helper()
	rb, _ := grumpkin.RandomScalar()
	ct, err := ballot.Encrypt(pk, ballot.Pack(r.Values()), rb)
	if err != nil {
		t.Fatal(err)
	}
	c0, _ := ct.C0.Compress()
	zero := "0x" + hex.EncodeToString(make([]byte, 32))
	b := Ballot{V: Version, Event: 1, Setup: 1, Root: zero, Nullifier: nullifier, Seq: seq, C0: "0x" + hex.EncodeToString(c0[:]), Proof: "AA=="}
	for j := range ct.C {
		cj, _ := ct.C[j].Compress()
		b.C = append(b.C, "0x"+hex.EncodeToString(cj[:]))
	}
	body, _ := Canonical(b)
	return body, ct
}

func TestTallierPublishesAfterKRealChanges(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewStore(pool)
	keys, err := GenerateKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := keys.Public().ParsedKeys()
	setup := Setup{V: Version, Event: 1, Version: 1, Blank: 7, NCap: 10, Mask: 255, PK: keys.PK, PKPoK: keys.PoK, NostrPubkey: testNostrPubkey, TallyPubkey: keys.TallyPubkey,
		VKHash: "0x01", Circuit: Circuit{SourceHash: "0x02"}, Relays: []Relay{{URL: "wss://a.test", State: "active"}},
		KReal: 3, RMin: 32, AnchorAge: DefaultAnchorAge, CheckpointPeriod: 20, Validity: 3600, Closes: time.Now().Add(24 * time.Hour)}
	for i := 0; i < 10; i++ {
		o := Option{Slot: i, Kind: "reserved"}
		if i < 7 {
			o = Option{Slot: i, Kind: "candidate", PersonID: int64(i + 1), Name: string(rune('A' + i))}
		} else if i == 7 {
			o = Option{Slot: i, Kind: "blank"}
		}
		setup.Options = append(setup.Options, o)
	}
	body, _ := Canonical(setup)
	if _, err := store.Log.Append(ctx, KindSetup, body); err != nil {
		t.Fatal(err)
	}
	// Four voters; one recasts the same ranking (not a change) and one
	// changes their mind.
	nul := func(i byte) string {
		b := make([]byte, 32)
		b[31] = i
		return "0x" + hex.EncodeToString(b)
	}
	rankings := map[string][]ballot.Ranking{
		nul(1): {{Len: 2, Slots: [3]int{4, 2}}},
		nul(2): {{Len: 1, Slots: [3]int{4}}, {Len: 1, Slots: [3]int{4}}},
		nul(3): {{Len: 1, Slots: [3]int{ballot.Blank}}},
		nul(4): {{Len: 1, Slots: [3]int{0}}, {Len: 3, Slots: [3]int{2, 4, 0}}},
	}
	var expectedCts []ballot.Ciphertext
	idx := int64(0)
	for n, rs := range rankings {
		for i, r := range rs {
			body, ct := encryptedBallot(t, pk, n, uint32(i+1), r)
			seq, err := store.Log.Append(ctx, KindBallot, body)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AddBallot(ctx, LoggedBallot{EventID: 1, Nullifier: n, Seq: uint32(i + 1), PIHash: "0x" + n + string(rune('0'+i)), LogSeq: seq, Checkpoint: 1, Body: body, NostrID: n + string(rune('0'+i)), Relay: "wss://a.test"}); err != nil {
				t.Fatal(err)
			}
			idx = seq
			if i == len(rs)-1 {
				expectedCts = append(expectedCts, ct)
			}
		}
	}
	if err := store.AddCheckpoint(ctx, Checkpoint{Index: 1, LogSize: idx, TreeRoot: "0x00", TreeSize: 4, Note: "n"}); err != nil {
		t.Fatal(err)
	}
	tl, err := NewTallier(ctx, store, &polls.Event{ID: 1, Slug: "test-vote"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tl.Release(ctx) })
	if len(tl.changed) != 4 {
		t.Fatalf("%d changed pseudonyms, want 4", len(tl.changed))
	}
	tl.rand = func() float64 { return 0.9 } // fire at the first chance
	// Step 1 arms at checkpoint 1; nothing fires at the same checkpoint.
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if last, _ := store.LastTally(ctx, 1); last != nil {
		t.Fatal("fired at the arming checkpoint")
	}
	if err := store.AddCheckpoint(ctx, Checkpoint{Index: 2, LogSize: idx, TreeRoot: "0x00", TreeSize: 4, Note: "n"}); err != nil {
		t.Fatal(err)
	}
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	last, err := store.LastTally(ctx, 1)
	if err != nil || last == nil {
		t.Fatalf("no tally: %v", err)
	}
	values, err := VerifyTally(last.Body, pk, expectedCts)
	if err != nil {
		t.Fatal(err)
	}
	res := ComputeResults(values, &setup)
	if res.Voters != 4 {
		t.Fatalf("voters %d", res.Voters)
	}
	// First choices: 4 twice, blank once, 2 once.
	first := map[int]int64{}
	for _, o := range res.Options {
		first[o.Slot] = o.First
	}
	if first[4] != 2 || first[ballot.Blank] != 1 || first[2] != 1 || first[0] != 0 {
		t.Fatalf("first choices %v", first)
	}
	// Option 4 beats everyone: it is ranked by 3 voters and above 2 by two
	// of them (one ranks 2 first).
	if res.Winner == nil || *res.Winner != 4 {
		t.Fatalf("winner %v", res.Winner)
	}
	if res.Options[0].Slot != 4 || res.Options[0].Rank != 1 {
		t.Fatalf("order %+v", res.Options)
	}
	// Nothing pending: no second tally.
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.LastTally(ctx, 1); again.Seq != last.Seq {
		t.Fatal("a second tally without changes")
	}
	// The close: one more ballot, then time passes the close; the final
	// tally covers it (no K_REAL) and the closed entry names it.
	body2, _ := encryptedBallot(t, pk, nul(1), 2, ballot.Ranking{Len: 1, Slots: [3]int{6}})
	seq, _ := store.Log.Append(ctx, KindBallot, body2)
	if err := store.AddBallot(ctx, LoggedBallot{EventID: 1, Nullifier: nul(1), Seq: 2, PIHash: "0xlast", LogSeq: seq, Checkpoint: 2, Body: body2, NostrID: "last", Relay: "wss://a.test"}); err != nil {
		t.Fatal(err)
	}
	tl.now = func() time.Time { return setup.Closes.Add(time.Minute) }
	// Without the ingest's snapshot the tallier waits.
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.ClosedEntry(ctx, 1); c != nil {
		t.Fatal("closed before the snapshot")
	}
	snapBody, _ := Canonical(Snapshot{V: Version, Event: 1, At: setup.Closes, Root: SnapshotRoot([]string{"a", "b"}), Count: 2, Checkpoint: 2})
	snapSeq, _ := store.Log.Append(ctx, KindSnapshot, snapBody)
	// A ballot logged after the snapshot (a relay's late copy, rejected in
	// the real ingest) is not in the final tally.
	body3, _ := encryptedBallot(t, pk, nul(2), 3, ballot.Ranking{Len: 1, Slots: [3]int{1}})
	seq3, _ := store.Log.Append(ctx, KindBallot, body3)
	if err := store.AddBallot(ctx, LoggedBallot{EventID: 1, Nullifier: nul(2), Seq: 3, PIHash: "0xlate", LogSeq: seq3, Checkpoint: 3, Body: body3, NostrID: "late", Relay: "wss://a.test"}); err != nil {
		t.Fatal(err)
	}
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	closed, err := store.ClosedEntry(ctx, 1)
	if err != nil || closed == nil {
		t.Fatalf("no closed entry: %v", err)
	}
	final, _ := store.LastTally(ctx, 1)
	if final.Seq != closed.Final || final.Seq == last.Seq || final.Upto != snapSeq {
		t.Fatalf("final tally %d up to %d, closed names %d, snapshot at %d", final.Seq, final.Upto, closed.Final, snapSeq)
	}
	if closed.SnapshotRoot != SnapshotRoot([]string{"b", "a"}) {
		t.Fatalf("closed names root %s", closed.SnapshotRoot)
	}
	// The late ballot is not counted; its pseudonym's earlier ballot is.
	if final.Body.Count != 4 {
		t.Fatalf("final tally counts %d ballots", final.Body.Count)
	}
	if err := tl.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if c2, _ := store.ClosedEntry(ctx, 1); c2.Final != closed.Final {
		t.Fatal("closed twice")
	}
}
