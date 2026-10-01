package contradiction

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
)

func pairIDs(pairs []pairInfo) [][2]int64 {
	out := make([][2]int64, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, [2]int64{p.factAID, p.factBID})
	}
	return out
}

func TestPairDedupeDropsSelfAndRepeats(t *testing.T) {
	in := []pairInfo{
		{factAID: 1, factBID: 2, similarity: 0.9},
		{factAID: 3, factBID: 3}, // self-pair from ancestor walking
		{factAID: 2, factBID: 1}, // same pair, swapped order
		{factAID: 1, factBID: 4},
		{factAID: 4, factBID: 1, queued: true}, // queued repeat marks the kept pair
	}
	got := dedupePairs("p", in)

	want := [][2]int64{{1, 2}, {1, 4}}
	if g := pairIDs(got); len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("dedupePairs = %v, want %v", g, want)
	}
	if got[0].similarity != 0.9 {
		t.Errorf("first occurrence must be kept (similarity lost: %v)", got[0].similarity)
	}
	if got[0].queued {
		t.Errorf("pair 1-2 must not be queued")
	}
	if !got[1].queued {
		t.Errorf("pair 1-4 must carry the queued flag of its repeat")
	}
}

func TestPairDropExternalOnlyKeepsDirectAndQueued(t *testing.T) {
	verbs := map[int64]string{
		1: "WAS_SENTENCED",  // external
		2: "RECEIVED_FUNDS", // external
		3: "CLAIMED",        // direct
		4: "",               // unknown
	}
	in := []pairInfo{
		{factAID: 1, factBID: 2},               // both external: dropped
		{factAID: 1, factBID: 3},               // one direct: kept
		{factAID: 1, factBID: 4},               // external + unknown: dropped
		{factAID: 2, factBID: 4, queued: true}, // queued bypasses the filter
	}
	got := pairIDs(dropExternalOnlyPairs("p", in, verbs))
	want := [][2]int64{{1, 3}, {2, 4}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("dropExternalOnlyPairs = %v, want %v", got, want)
	}
}

func TestPairDropCheckedIsOrderIndependent(t *testing.T) {
	checked := map[string]bool{"1-2": true, "3-4": true}
	in := []pairInfo{
		{factAID: 2, factBID: 1}, // checked, stored smaller-first
		{factAID: 3, factBID: 4}, // checked
		{factAID: 2, factBID: 5}, // new
	}
	got := pairIDs(dropCheckedPairs("p", in, checked))
	if len(got) != 1 || got[0] != [2]int64{2, 5} {
		t.Fatalf("dropCheckedPairs = %v, want [[2 5]]", got)
	}
}

func TestPairParaphraseSkip(t *testing.T) {
	cases := []struct {
		name             string
		verbA, verbB     string
		similarity       float64
		skip, bothExtern bool
	}{
		{"same verb above floor", "CLAIMED", "CLAIMED", 0.85, true, false},
		{"same verb at floor", "CLAIMED", "CLAIMED", 0.80, false, false},
		{"both external above floor", "WAS_SENTENCED", "WAS_FINED", 0.9, true, true},
		{"different direct verbs", "CLAIMED", "DENIED", 0.95, false, false},
		{"missing verb", "", "CLAIMED", 0.95, false, false},
		{"unknown verbs count as external", "MUMBLED", "WHISPERED", 0.9, true, true},
	}
	for _, tc := range cases {
		skip, bothExternal := paraphraseSkip(tc.verbA, tc.verbB, tc.similarity)
		if skip != tc.skip || bothExternal != tc.bothExtern {
			t.Errorf("%s: paraphraseSkip = (%v, %v), want (%v, %v)", tc.name, skip, bothExternal, tc.skip, tc.bothExtern)
		}
	}
}

func TestDetectFirstNBoundsPersons(t *testing.T) {
	persons := []storage.PersonInfo{{ID: 1}, {ID: 2}, {ID: 3}}
	if got := firstN(persons, 0); len(got) != 3 {
		t.Errorf("max 0 must keep all persons, got %d", len(got))
	}
	if got := firstN(persons, 5); len(got) != 3 {
		t.Errorf("max above length must keep all persons, got %d", len(got))
	}
	got := firstN(persons, 2)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("max 2 must keep the first two in order, got %v", got)
	}
	ids := firstN([]int64{7, 8, 9}, 1)
	if len(ids) != 1 || ids[0] != 7 {
		t.Errorf("firstN on ids = %v, want [7]", ids)
	}
}

type fakeVerifier struct {
	keep bool
	err  error
}

func (f fakeVerifier) VerifyContradiction(context.Context, promptrules.Statement, promptrules.Statement, string) (bool, string, error) {
	return f.keep, "", f.err
}

type fakeSameClaim struct {
	same  bool
	err   error
	calls *int
}

func (f fakeSameClaim) IsSameClaim(context.Context, string, string, string) (bool, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.same, f.err
}

// TestDecidePair: an LLM that goes away between the judge and its second
// gate leaves the pair undecided (nothing recorded, judged again next run)
// and stops the run; other gate failures keep their documented behavior.
func TestDecidePair(t *testing.T) {
	ctx := context.Background()
	down := fmt.Errorf("%w: primary down", llm.ErrUnavailable)
	boom := errors.New("unparseable")
	pair := pairInfo{factAID: 1, factAText: "a", factBID: 2, factBText: "b", similarity: 0.9}
	yes := BatchResult{IsContradictory: true, Reason: "r"}
	no := BatchResult{}

	cases := []struct {
		name     string
		result   BatchResult
		verifier fakeVerifier
		dedupe   sameClaimJudge
		want     pairDecision
		wantDown bool
	}{
		{name: "verified yes", result: yes, verifier: fakeVerifier{keep: true},
			want: pairDecision{decided: true, contradiction: true}},
		{name: "refuted yes is a checked no", result: yes, verifier: fakeVerifier{keep: false},
			want: pairDecision{decided: true}},
		{name: "verifier unavailable after a yes: nothing recorded", result: yes, verifier: fakeVerifier{err: down},
			wantDown: true},
		{name: "verifier fails otherwise: yes kept (fail-open)", result: yes, verifier: fakeVerifier{err: boom},
			want: pairDecision{decided: true, contradiction: true}},
		{name: "detector unavailable", result: BatchResult{Error: down}, wantDown: true},
		{name: "detector fails otherwise: pair left pending", result: BatchResult{Error: boom}},
		{name: "no without dedup net", result: no, want: pairDecision{decided: true}},
		{name: "same claim", result: no, dedupe: fakeSameClaim{same: true},
			want: pairDecision{decided: true, sameClaim: true}},
		{name: "dedup net unavailable after a no: nothing recorded", result: no, dedupe: fakeSameClaim{err: down},
			wantDown: true},
		{name: "dedup net fails otherwise: pair left pending", result: no, dedupe: fakeSameClaim{err: boom}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decidePair(ctx, c.verifier, c.dedupe, "p", pair, c.result, false)
			if c.wantDown != errors.Is(err, llm.ErrUnavailable) || (!c.wantDown && err != nil) {
				t.Fatalf("err = %v, want unavailable=%v", err, c.wantDown)
			}
			if got != c.want {
				t.Errorf("decision = %+v, want %+v", got, c.want)
			}
		})
	}

	t.Run("cut by the deadline: nothing recorded, the run stops", func(t *testing.T) {
		expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
		for name, c := range map[string]struct {
			result   BatchResult
			verifier fakeVerifier
			dedupe   sameClaimJudge
		}{
			"detector":  {result: BatchResult{Error: expired.Err()}},
			"verifier":  {result: yes, verifier: fakeVerifier{err: expired.Err()}},
			"dedup net": {result: no, dedupe: fakeSameClaim{err: expired.Err()}},
		} {
			got, err := decidePair(expired, c.verifier, c.dedupe, "p", pair, c.result, false)
			if !errors.Is(err, context.DeadlineExceeded) || got.decided {
				t.Errorf("%s: got (%+v, %v); want no decision and the deadline", name, got, err)
			}
		}
	})

	t.Run("dry run never asks the dedup net", func(t *testing.T) {
		calls := 0
		got, err := decidePair(ctx, fakeVerifier{}, fakeSameClaim{same: true, calls: &calls}, "p", pair, no, true)
		if err != nil || got != (pairDecision{decided: true}) || calls != 0 {
			t.Fatalf("got (%+v, %v, calls=%d)", got, err, calls)
		}
	})
}
