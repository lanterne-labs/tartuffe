package core

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// foldClusterStorer is a FactFoldStorer over in-memory facts: no shared
// facts, no contradictions, a scripted trigram pair list, and evidence on
// every pair except the ones in noEvidence.
type foldClusterStorer struct {
	GroupStorer
	facts      []FactForFold
	textPairs  [][2]int64
	noEvidence [][2]int64
}

func (f *foldClusterStorer) GetFoldFactsForPerson(ctx context.Context, personID int64) ([]FactForFold, error) {
	return f.facts, nil
}
func (f *foldClusterStorer) ListContradictionFactPairs(ctx context.Context, personID int64) ([]ContradictionFactPair, error) {
	return nil, nil
}
func (f *foldClusterStorer) ListTextSimilarFactPairs(ctx context.Context, factIDs []int64, floor float64) ([][2]int64, error) {
	return f.textPairs, nil
}
func (f *foldClusterStorer) LoadClaimSources(ctx context.Context, factIDs []int64, sourceTypes []string) (map[int64][]ClaimSource, error) {
	return nil, nil
}
func (f *foldClusterStorer) ListGroupEvidencePairs(ctx context.Context, pairs [][2]int64, rule GroupEvidence) ([][2]int64, error) {
	var out [][2]int64
	for _, p := range pairs {
		if !slices.Contains(f.noEvidence, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// sameAlwaysJudge groups every pair; a pair with the text "C+" on one side
// is a contained pair.
type sameAlwaysJudge struct{ asked [][2]string }

func (j *sameAlwaysJudge) IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error) {
	j.asked = append(j.asked, [2]string{a.Text, b.Text})
	if a.Text == "C+" || b.Text == "C+" {
		return OccurrenceContained, nil
	}
	return OccurrenceSame, nil
}

// A near-verbatim restatement whose embedding is far away is only a fold
// candidate through the trigram source; the gate still decides every pair,
// and the cluster is a clique (C must also pass against B).
func TestFoldClustersUnionTextPairs(t *testing.T) {
	store := &foldClusterStorer{facts: []FactForFold{
		{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}},
		{ID: 2, Text: "B", Embedding: []float32{0.9, 0.436, 0}}, // cos ≈ 0.90 with A
		{ID: 3, Text: "C", Embedding: []float32{0, 0, 1}},       // orthogonal to both
	}}
	p := PersonRecord{ID: 1, Name: "p"}

	judge := &sameAlwaysJudge{}
	clusters, err := FoldClustersForPerson(context.Background(), store, judge, p, FoldOptions{Floor: 0.70, MaxCluster: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || len(clusters[0].Facts) != 2 || len(judge.asked) != 1 {
		t.Fatalf("embedding only: clusters=%v asked=%d, want one {A,B} cluster from one gate call", clusters, len(judge.asked))
	}

	store.textPairs = [][2]int64{{1, 3}}
	judge = &sameAlwaysJudge{}
	clusters, err = FoldClustersForPerson(context.Background(), store, judge, p, FoldOptions{Floor: 0.70, TextFloor: 0.40, MaxCluster: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || len(clusters[0].Facts) != 2 || len(judge.asked) != 2 {
		t.Fatalf("with text pairs: clusters=%v asked=%d, want {A,B} only (C never confirmed against B) from two gate calls", clusters, len(judge.asked))
	}
}

// A candidate pair that does not meet the evidence rule never reaches the
// gate and never groups, however close the embeddings are.
func TestFoldClustersNeedEvidence(t *testing.T) {
	store := &foldClusterStorer{
		facts: []FactForFold{
			{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}},
			{ID: 2, Text: "B", Embedding: []float32{0.9, 0.436, 0}}, // cos ≈ 0.90 with A
		},
		noEvidence: [][2]int64{{1, 2}},
	}
	judge := &sameAlwaysJudge{}
	clusters, err := FoldClustersForPerson(context.Background(), store, judge, PersonRecord{ID: 1, Name: "p"}, DefaultFoldOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 0 || len(judge.asked) != 0 {
		t.Fatalf("clusters=%v asked=%d, want no cluster and no gate call", clusters, len(judge.asked))
	}
}

// A cluster with a contained pair takes its fullest fact as the parent.
func TestApplyFoldClusterContainedParent(t *testing.T) {
	gs := &foldFakeStorer{}
	store := &foldClusterStorer{GroupStorer: gs, facts: []FactForFold{
		{ID: 1, Text: "C", Embedding: []float32{1, 0, 0}},
		{ID: 2, Text: "C+", Embedding: []float32{1, 0, 0}},
	}}
	p := PersonRecord{ID: 1, Name: "p"}
	clusters, err := FoldClustersForPerson(context.Background(), store, &sameAlwaysJudge{}, p, DefaultFoldOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || !clusters[0].Contained {
		t.Fatalf("clusters=%+v, want one contained cluster", clusters)
	}
	if _, err := ApplyFoldCluster(context.Background(), store, p, clusters[0]); err != nil {
		t.Fatal(err)
	}
	if len(gs.merges) != 1 || gs.merges[0] != [2]int64{1, 2} {
		t.Fatalf("merges=%v, want fact 1 under the fuller fact 2", gs.merges)
	}
}

// A pair of two facts inserted under the current rule keeps its insert-time
// verdict: the gate is asked only about pairs with an older fact.
func TestFoldClustersSkipsPairsJudgedAtInsert(t *testing.T) {
	since := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	old, recent := since.Add(-time.Hour), since.Add(time.Hour)
	store := &foldClusterStorer{facts: []FactForFold{
		{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}, CreatedAt: old},
		{ID: 2, Text: "B", Embedding: []float32{1, 0, 0}, CreatedAt: recent},
		{ID: 3, Text: "C", Embedding: []float32{1, 0, 0}, CreatedAt: recent},
	}}
	judge := &sameAlwaysJudge{}
	opts := FoldOptions{Floor: 0.70, MaxCluster: 12, JudgedSince: since}
	if _, err := FoldClustersForPerson(context.Background(), store, judge, PersonRecord{ID: 1, Name: "p"}, opts); err != nil {
		t.Fatal(err)
	}
	for _, a := range judge.asked {
		if a == [2]string{"B", "C"} || a == [2]string{"C", "B"} {
			t.Fatalf("the gate was asked about B-C, two facts judged at insertion: %v", judge.asked)
		}
	}
	if len(judge.asked) != 2 {
		t.Fatalf("asked %v, want A-B and A-C", judge.asked)
	}
}

// TestCliquesKeepLeftovers: a chain of reports (1-2-3 joinable together, 4-5
// joinable together, 3-4 the bridge) splits into two clusters; the old single
// clique kept {1,2,3} and dropped 4 and 5 for good (prod: the Le Monde and RTL
// reports of one conviction, judged the same occurrence, never grouped).
func TestCliquesKeepLeftovers(t *testing.T) {
	f := func(id int64) FactForFold { return FactForFold{ID: id} }
	join := map[[2]int64]bool{{1, 2}: true, {1, 3}: true, {2, 3}: true, {3, 4}: true, {4, 5}: true}
	got := cliques([]FactForFold{f(1), f(2), f(3), f(4), f(5)}, func(x, y FactForFold) bool {
		return join[OrderedPair(x.ID, y.ID)]
	})
	ids := func(c []FactForFold) []int64 {
		var out []int64
		for _, x := range c {
			out = append(out, x.ID)
		}
		return out
	}
	if len(got) != 2 || fmt.Sprint(ids(got[0])) != "[1 2 3]" || fmt.Sprint(ids(got[1])) != "[4 5]" {
		var all [][]int64
		for _, c := range got {
			all = append(all, ids(c))
		}
		t.Errorf("cliques = %v, want [[1 2 3] [4 5]]", all)
	}
	// A lone leftover is no cluster.
	if got := cliques([]FactForFold{f(1), f(2), f(9)}, func(x, y FactForFold) bool { return join[OrderedPair(x.ID, y.ID)] }); len(got) != 1 {
		t.Errorf("a lone leftover made a cluster: %v", got)
	}
}
