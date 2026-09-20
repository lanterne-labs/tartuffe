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

func (r *reviewStorer) ListMetaFacts(ctx context.Context, since time.Time) ([]MetaFactRef, error) {
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
// word; "err" as a text makes the gate fail.
type prefixJudge struct{}

func (prefixJudge) IsSamePosition(ctx context.Context, subject, a, b string) (bool, error) {
	if a == "err" || b == "err" {
		return false, errors.New("gate down")
	}
	return strings.Fields(a)[0] == strings.Fields(b)[0], nil
}

type joinSynth struct{}

func (joinSynth) SynthesizeMetaFact(ctx context.Context, subject string, texts []string, lang string) (string, error) {
	return strings.Join(texts, " + "), nil
}

func TestReviewMetaFacts(t *testing.T) {
	store := &reviewStorer{
		metas: []MetaFactRef{{ID: 1, PersonName: "p"}, {ID: 2, PersonName: "p"}, {ID: 3, PersonName: "p"}, {ID: 4, PersonName: "p"}},
		members: map[int64][]FactForFold{
			1: {{ID: 10, Text: "tax cut A"}, {ID: 11, Text: "tax cut B"}},                                 // intact
			2: {{ID: 20, Text: "tax cut A"}, {ID: 21, Text: "war must end"}, {ID: 22, Text: "tax cut C"}}, // trimmed: 21 out
			3: {{ID: 30, Text: "tax cut A"}, {ID: 31, Text: "war must end"}},                              // dissolved
			4: {{ID: 40, Text: "err"}, {ID: 41, Text: "tax cut"}},                                         // gate failure
		},
		texts: map[int64]string{},
	}

	st, err := ReviewMetaFacts(context.Background(), store, prefixJudge{}, joinSynth{}, MetaReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := MetaReviewStats{Metas: 4, Intact: 1, Trimmed: 1, Dissolved: 1, GateFailures: 1, DissolvedIDs: []int64{3}}
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
	if got := store.texts[2]; got != "tax cut A + tax cut C" {
		t.Fatalf("meta 2 text = %q, want it re-synthesized from the clique", got)
	}
	if _, touched := store.texts[3]; touched {
		t.Fatal("a dissolved meta must be left untouched")
	}
}
