package contradiction

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/core"
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
		{"two ballots are judged", "VOTED_FOR", "VOTED_FOR", 0.95, false, false},
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

type fakeOccurrence struct {
	verdict core.OccurrenceVerdict
	err     error
	calls   *int
}

func (f fakeOccurrence) IsSameOccurrence(_ context.Context, a, b core.ClaimEvidence) (core.OccurrenceVerdict, error) {
	if a.Text != "a" || b.Text != "b" {
		panic(fmt.Sprintf("the gate must read the evidence of A then B, got %q and %q", a.Text, b.Text))
	}
	if f.calls != nil {
		*f.calls++
	}
	return f.verdict, f.err
}

// TestDecidePair: an LLM that goes away between the judge and its second
// gate leaves the pair undecided (nothing recorded, judged again next run)
// and stops the run; other gate failures keep their documented behavior.
func TestDecidePair(t *testing.T) {
	ctx := context.Background()
	down := fmt.Errorf("%w: primary down", llm.ErrUnavailable)
	boom := errors.New("unparseable")
	pair := pairInfo{factAID: 1, factAText: "a", factBID: 2, factBText: "b", similarity: 0.9,
		evidence: &[2]core.ClaimEvidence{{Text: "a"}, {Text: "b"}}}
	yes := BatchResult{IsContradictory: true, Reason: "r"}
	no := BatchResult{}

	cases := []struct {
		name     string
		result   BatchResult
		verifier fakeVerifier
		judge    core.SameOccurrenceJudge
		want     pairDecision
		wantDown bool
	}{
		{name: "verified yes", result: yes, verifier: fakeVerifier{keep: true},
			want: pairDecision{decided: true, contradiction: true}},
		{name: "refuted yes is a checked no", result: yes, verifier: fakeVerifier{keep: false},
			want: pairDecision{decided: true}},
		{name: "verifier unavailable after a yes: nothing recorded", result: yes, verifier: fakeVerifier{err: down},
			wantDown: true},
		{name: "verifier fails otherwise: pair left pending, nothing unverified", result: yes, verifier: fakeVerifier{err: boom}},
		{name: "detector unavailable", result: BatchResult{Error: down}, wantDown: true},
		{name: "detector fails otherwise: pair left pending", result: BatchResult{Error: boom}},
		{name: "no without grouping gate", result: no, want: pairDecision{decided: true}},
		{name: "one occurrence", result: no, judge: fakeOccurrence{verdict: core.OccurrenceSame},
			want: pairDecision{decided: true, occurrence: core.OccurrenceSame}},
		{name: "one occasion, one fact contains the other", result: no, judge: fakeOccurrence{verdict: core.OccurrenceContained},
			want: pairDecision{decided: true, occurrence: core.OccurrenceContained}},
		{name: "two occurrences", result: no, judge: fakeOccurrence{verdict: core.OccurrenceDifferent},
			want: pairDecision{decided: true}},
		{name: "grouping gate unavailable after a no: nothing recorded", result: no, judge: fakeOccurrence{err: down},
			wantDown: true},
		{name: "grouping gate fails otherwise: pair left pending", result: no, judge: fakeOccurrence{err: boom}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decidePair(ctx, c.verifier, c.judge, "p", pair, c.result, false)
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
			judge    core.SameOccurrenceJudge
		}{
			"detector":      {result: BatchResult{Error: expired.Err()}},
			"verifier":      {result: yes, verifier: fakeVerifier{err: expired.Err()}},
			"grouping gate": {result: no, judge: fakeOccurrence{err: expired.Err()}},
		} {
			got, err := decidePair(expired, c.verifier, c.judge, "p", pair, c.result, false)
			if !errors.Is(err, context.DeadlineExceeded) || got.decided {
				t.Errorf("%s: got (%+v, %v); want no decision and the deadline", name, got, err)
			}
		}
	})

	t.Run("dry run never asks the grouping gate", func(t *testing.T) {
		calls := 0
		got, err := decidePair(ctx, fakeVerifier{}, fakeOccurrence{verdict: core.OccurrenceSame, calls: &calls}, "p", pair, no, true)
		if err != nil || got != (pairDecision{decided: true}) || calls != 0 {
			t.Fatalf("got (%+v, %v, calls=%d)", got, err, calls)
		}
	})

	t.Run("a pair without grouping evidence never asks the grouping gate", func(t *testing.T) {
		calls := 0
		bare := pair
		bare.evidence = nil
		got, err := decidePair(ctx, fakeVerifier{}, fakeOccurrence{verdict: core.OccurrenceSame, calls: &calls}, "p", bare, no, false)
		if err != nil || got != (pairDecision{decided: true}) || calls != 0 {
			t.Fatalf("got (%+v, %v, calls=%d)", got, err, calls)
		}
	})

	t.Run("a verified contradiction never asks the grouping gate", func(t *testing.T) {
		calls := 0
		got, err := decidePair(ctx, fakeVerifier{keep: true}, fakeOccurrence{verdict: core.OccurrenceSame, calls: &calls}, "p", pair, yes, false)
		if err != nil || got != (pairDecision{decided: true, contradiction: true}) || calls != 0 {
			t.Fatalf("got (%+v, %v, calls=%d)", got, err, calls)
		}
	})
}

// fakeEvidenceStorer meets the evidence rule for the pairs in met and has
// one source per fact.
type fakeEvidenceStorer struct {
	met    map[[2]int64]bool
	asked  [][2]int64
	loaded []int64
}

func (f *fakeEvidenceStorer) ListGroupEvidencePairs(_ context.Context, pairs [][2]int64, _ core.GroupEvidence) ([][2]int64, error) {
	f.asked = pairs
	var out [][2]int64
	for _, p := range pairs {
		if f.met[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeEvidenceStorer) LoadClaimSources(_ context.Context, ids []int64, _ []string) (map[int64][]core.ClaimSource, error) {
	f.loaded = ids
	out := map[int64][]core.ClaimSource{}
	for _, id := range ids {
		out[id] = []core.ClaimSource{{Outlet: fmt.Sprintf("outlet%d.fr", id)}}
	}
	return out, nil
}

// TestAttachOccurrenceEvidence: only near-identical pairs go to the evidence
// rule, and only the pairs that meet it get the evidence the gate reads.
func TestAttachOccurrenceEvidence(t *testing.T) {
	core.RequireQuoteForSourceType("news")
	pairs := []pairInfo{
		{factAID: 1, factAText: "a1", factBID: 2, factBText: "b2", similarity: 0.85}, // meets the rule
		{factAID: 3, factAText: "a3", factBID: 4, factBText: "b4", similarity: 0.90}, // no evidence
		{factAID: 5, factAText: "a5", factBID: 6, factBText: "b6", similarity: 0.70}, // below the floor
		{factAID: 7, factAText: "", factBID: 8, factBText: "b8", similarity: 0.95},   // fact deleted
	}
	storer := &fakeEvidenceStorer{met: map[[2]int64]bool{{1, 2}: true, {5, 6}: true}}
	if found, err := attachOccurrenceEvidence(context.Background(), storer, pairs); err != nil || !found {
		t.Fatalf("found = %v, err = %v; want a pair with evidence", found, err)
	}
	if want := [][2]int64{{1, 2}, {3, 4}}; fmt.Sprint(storer.asked) != fmt.Sprint(want) {
		t.Errorf("pairs given to the evidence rule = %v, want %v", storer.asked, want)
	}
	if fmt.Sprint(storer.loaded) != "[1 2]" {
		t.Errorf("sources loaded for %v, want [1 2]", storer.loaded)
	}
	e := pairs[0].evidence
	if e == nil || e[0].Text != "a1" || e[1].Text != "b2" ||
		len(e[0].Sources) != 1 || e[0].Sources[0].Outlet != "outlet1.fr" || e[1].Sources[0].Outlet != "outlet2.fr" {
		t.Fatalf("evidence of pair 1-2 = %+v", e)
	}
	for _, p := range pairs[1:] {
		if p.evidence != nil {
			t.Errorf("pair %d-%d must have no evidence", p.factAID, p.factBID)
		}
	}
}

// fakeGroupStorer records the group writes. Only the methods that
// core.ApplyFoldCluster calls are set; the others panic (nil interface).
type fakeGroupStorer struct {
	core.FactFoldStorer
	failMerge bool
	merges    [][2]int64 // from, into
}

func (f *fakeGroupStorer) MostConcreteFact(_ context.Context, ids []int64) (int64, error) {
	return min(ids[0], ids[1]), nil
}

func (f *fakeGroupStorer) MergeFactIntoGroup(_ context.Context, from, into int64) error {
	if f.failMerge {
		return errors.New("write failed")
	}
	f.merges = append(f.merges, [2]int64{from, into})
	return nil
}

func (f *fakeGroupStorer) RefreshPersonScore(context.Context, int64) error { return nil }

// TestPairGrouper: a fact gets one parent per run, a recorded contradiction
// is never grouped, and a failed write leaves the pair pending.
func TestPairGrouper(t *testing.T) {
	ctx := context.Background()
	pair := func(a, b int64) pairInfo {
		return pairInfo{factAID: a, factAText: "a", factBID: b, factBText: "b",
			evidence: &[2]core.ClaimEvidence{{Text: "a"}, {Text: "b"}}}
	}
	same := pairDecision{decided: true, occurrence: core.OccurrenceSame}
	storer := &fakeGroupStorer{}
	g := &pairGrouper{storer: storer, person: core.PersonRecord{ID: 1, Name: "p"},
		contradicted: map[[2]int64]int64{{20, 21}: 7}, members: map[int64]bool{}}

	// 11 becomes a member of 10; then 11 and 12 wait for the next run.
	for _, c := range []struct {
		p      pairInfo
		d      pairDecision
		record bool
	}{
		{pair(10, 11), same, true},
		{pair(11, 12), same, false},
		{pair(12, 11), same, false},
		{pair(10, 12), same, true}, // 10 is a parent: 12 joins it
		{pair(21, 20), same, true}, // recorded contradiction: not grouped, recorded
		{pair(30, 31), pairDecision{decided: true}, true},
		{pair(30, 32), pairDecision{decided: true, contradiction: true}, true},
		{pairInfo{factAID: 40, factBID: 41}, same, true}, // no evidence: never gated
	} {
		if got := g.apply(ctx, c.p, c.d); got != c.record {
			t.Errorf("pair %d-%d: record = %v, want %v", c.p.factAID, c.p.factBID, got, c.record)
		}
	}
	if want := [][2]int64{{11, 10}, {12, 10}}; fmt.Sprint(storer.merges) != fmt.Sprint(want) {
		t.Errorf("merges = %v, want %v", storer.merges, want)
	}
	if g.gated != 6 || g.grouped != 2 || g.skippedMember != 2 || g.skippedContradicted != 1 {
		t.Errorf("counts gated=%d grouped=%d member=%d contradicted=%d; want 6 2 2 1",
			g.gated, g.grouped, g.skippedMember, g.skippedContradicted)
	}

	storer.failMerge = true
	if g.apply(ctx, pair(50, 51), same) {
		t.Error("a failed group write must leave the pair pending")
	}
	if g.members[50] || g.members[51] {
		t.Error("a failed group write makes no member")
	}
}
