package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type reviewStorer struct {
	metas    []MetaFactRef
	members  map[int64][]FactForFold
	unfolded [][2]int64
	texts    map[int64]string
}

func (r *reviewStorer) ListMetaFacts(ctx context.Context, since time.Time, onlyIDs []int64) ([]MetaFactRef, error) {
	return r.metas, nil
}
func (r *reviewStorer) GetMetaMembers(ctx context.Context, metaID int64) ([]FactForFold, error) {
	return r.members[metaID], nil
}
func (r *reviewStorer) UnfoldFactFromMeta(ctx context.Context, metaID, factID int64) error {
	r.unfolded = append(r.unfolded, [2]int64{metaID, factID})
	return nil
}
func (r *reviewStorer) UpdateMetaFactText(ctx context.Context, metaID int64, text string) error {
	r.texts[metaID] = text
	return nil
}
func (r *reviewStorer) GetFactEmbeddingMeta(ctx context.Context, factID int64) (string, string, string, error) {
	return "", "fr", "", nil
}
func (r *reviewStorer) RefreshPersonScore(ctx context.Context, personID int64) error { return nil }

// prefixJudge: two texts are the same position when they share their first
// word; "err" as a text makes the gate fail; a "(flip)" text is judged
// different only as the second statement (order-dependent).
type prefixJudge struct{}

func (prefixJudge) IsSamePosition(ctx context.Context, subject, a, b string) (bool, error) {
	if a == "err" || b == "err" {
		return false, errors.New("gate down")
	}
	if strings.Contains(b, "(flip)") {
		return false, nil
	}
	// A "(odd)" text is a compound the gate is borderline on: it agrees with
	// the seed "tax cut A" and rejects every other sibling.
	if strings.Contains(a, "(odd)") || strings.Contains(b, "(odd)") {
		return a == "tax cut A" || b == "tax cut A", nil
	}
	return strings.Fields(a)[0] == strings.Fields(b)[0], nil
}

type joinSynth struct{}

func (joinSynth) SynthesizeMetaFact(ctx context.Context, subject string, texts []string, lang string) (string, error) {
	return strings.Join(texts, " + "), nil
}

func TestReviewMetaFacts(t *testing.T) {
	store := &reviewStorer{
		metas: []MetaFactRef{{ID: 1, PersonName: "p"}, {ID: 2, PersonName: "p"}, {ID: 3, PersonName: "p"}, {ID: 4, PersonName: "p"}, {ID: 5, PersonName: "p"}, {ID: 6, PersonName: "p"}},
		members: map[int64][]FactForFold{
			1: {{ID: 10, Text: "tax cut A"}, {ID: 11, Text: "tax cut B"}},                                 // intact
			2: {{ID: 20, Text: "tax cut A"}, {ID: 21, Text: "war must end"}, {ID: 22, Text: "tax cut C"}}, // trimmed: 21 out
			3: {{ID: 30, Text: "tax cut A"}, {ID: 31, Text: "war must end"}},                              // dissolved
			4: {{ID: 40, Text: "err"}, {ID: 41, Text: "tax cut"}},                                         // gate failure
			// Majority tolerance: the compound 52 gets in on a 1-1 tie (seed
			// yes, 51 no); 53 then agrees with 50 and 51 and fails only
			// against 52 — it stays. Under complete linkage it was evicted.
			5: {{ID: 50, Text: "tax cut A"}, {ID: 51, Text: "tax cut B"}, {ID: 52, Text: "tax cut (odd)"}, {ID: 53, Text: "tax cut D"}},
			// Order-swap: the gate rejects 61 as the second statement but
			// accepts it as the first — not a stable "different"; intact.
			6: {{ID: 60, Text: "tax cut A"}, {ID: 61, Text: "tax cut (flip)"}},
		},
		texts: map[int64]string{},
	}

	st, err := ReviewMetaFacts(context.Background(), store, prefixJudge{}, joinSynth{}, MetaReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := MetaReviewStats{Metas: 6, Intact: 3, Trimmed: 1, Dissolved: 1, GateFailures: 1, DissolvedIDs: []int64{3}}
	if st.Metas != want.Metas || st.Intact != want.Intact || st.Trimmed != want.Trimmed || st.Dissolved != want.Dissolved ||
		st.GateFailures != want.GateFailures || st.Unfolded != 0 || len(st.DissolvedIDs) != 1 || st.DissolvedIDs[0] != 3 {
		t.Fatalf("dry run stats = %+v, want %+v", st, want)
	}
	if len(store.unfolded) != 0 || len(store.texts) != 0 {
		t.Fatalf("dry run wrote: unfolded=%v texts=%v", store.unfolded, store.texts)
	}

	st, err = ReviewMetaFacts(context.Background(), store, prefixJudge{}, joinSynth{}, MetaReviewOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Unfolded != 1 || len(store.unfolded) != 1 || store.unfolded[0] != [2]int64{2, 21} {
		t.Fatalf("apply: unfolded=%v stats=%+v, want fact 21 unfolded from meta 2 only", store.unfolded, st)
	}
	if _, touched := store.texts[5]; touched {
		t.Fatal("meta 5 is intact by majority; it must not be rewritten")
	}
	if got := store.texts[2]; got != "tax cut A + tax cut C" {
		t.Fatalf("meta 2 text = %q, want it re-synthesized from the clique", got)
	}
	if _, touched := store.texts[3]; touched {
		t.Fatal("a dissolved meta must be left untouched")
	}
}
