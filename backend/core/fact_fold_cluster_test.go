package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"digital-mirror/backend/llm"
)

// foldClusterStorer is a FactFoldStorer over in-memory facts: no shared
// facts, no contradictions, a scripted trigram pair list, and evidence on
// every pair except the ones in noEvidence.
type foldClusterStorer struct {
	GroupStorer
	facts      []FactForFold
	textPairs  [][2]int64
	noEvidence [][2]int64
	checks     map[[2]int64]OccurrenceVerdict // the stored gate verdicts
}

func (f *foldClusterStorer) LoadOccurrenceChecks(ctx context.Context, pairs [][2]int64, ruleHash string) (map[[2]int64]OccurrenceVerdict, error) {
	out := map[[2]int64]OccurrenceVerdict{}
	for _, p := range pairs {
		if v, ok := f.checks[p]; ok {
			out[p] = v
		}
	}
	return out, nil
}
func (f *foldClusterStorer) RecordOccurrenceCheck(ctx context.Context, a, b int64, ruleHash string, v OccurrenceVerdict) error {
	if f.checks == nil {
		f.checks = map[[2]int64]OccurrenceVerdict{}
	}
	f.checks[OrderedPair(a, b)] = v
	return nil
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
	store.checks = nil // the first run stored A-B; ask it again here
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

// A pair the gate already judged keeps its stored verdict: the gate is asked
// only about the other pairs, and its answers are stored. AskAll asks again.
func TestFoldClustersReuseStoredVerdicts(t *testing.T) {
	store := &foldClusterStorer{
		facts: []FactForFold{
			{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}},
			{ID: 2, Text: "B", Embedding: []float32{1, 0, 0}},
			{ID: 3, Text: "C", Embedding: []float32{1, 0, 0}},
		},
		checks: map[[2]int64]OccurrenceVerdict{{2, 3}: OccurrenceDifferent},
	}
	judge := &sameAlwaysJudge{}
	var asked atomic.Int64
	opts := FoldOptions{Floor: 0.70, MaxCluster: 12, Asked: &asked}
	clusters, err := FoldClustersForPerson(context.Background(), store, judge, PersonRecord{ID: 1, Name: "p"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range judge.asked {
		if a == [2]string{"B", "C"} || a == [2]string{"C", "B"} {
			t.Fatalf("the gate was asked about B-C, a stored verdict: %v", judge.asked)
		}
	}
	if len(judge.asked) != 2 || asked.Load() != 2 {
		t.Fatalf("asked %v (counted %d), want A-B and A-C", judge.asked, asked.Load())
	}
	if store.checks[[2]int64{1, 2}] != OccurrenceSame || store.checks[[2]int64{1, 3}] != OccurrenceSame {
		t.Errorf("checks %v, want the two answers stored", store.checks)
	}
	// B-C stays different, so the clique is {A,B} or {A,C}, never all three.
	if len(clusters) != 1 || len(clusters[0].Facts) != 2 {
		t.Errorf("clusters %v, want one pair", clusters)
	}

	judge = &sameAlwaysJudge{}
	opts = FoldOptions{Floor: 0.70, MaxCluster: 12, AskAll: true}
	if _, err := FoldClustersForPerson(context.Background(), store, judge, PersonRecord{ID: 1, Name: "p"}, opts); err != nil {
		t.Fatal(err)
	}
	if len(judge.asked) != 3 {
		t.Fatalf("AskAll asked %v, want the three pairs", judge.asked)
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

// downJudge is an LLM that cannot serve.
type downJudge struct{}

func (downJudge) IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error) {
	return OccurrenceDifferent, fmt.Errorf("gate: %w", llm.ErrUnavailable)
}

// An LLM that cannot serve stops the fold: going on would read every pair as
// distinct and fold nothing (prod 2026-10-23, every seat at its limit).
func TestFoldClustersStopWhenTheLLMIsDown(t *testing.T) {
	store := &foldClusterStorer{facts: []FactForFold{
		{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}},
		{ID: 2, Text: "B", Embedding: []float32{1, 0, 0}},
	}}
	_, err := FoldClustersForPerson(context.Background(), store, downJudge{}, PersonRecord{ID: 1, Name: "p"}, DefaultFoldOptions)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v, want llm.ErrUnavailable", err)
	}
}

// sweepFoldStorer lists two persons to fold and records the folds.
type sweepFoldStorer struct {
	*foldClusterStorer
	folded []int64
}

func (s *sweepFoldStorer) ListPersonsNeedingFold(ctx context.Context, ruleHash string) ([]PersonRecord, error) {
	return []PersonRecord{{ID: 1, Name: "p"}, {ID: 2, Name: "q"}}, nil
}
func (s *sweepFoldStorer) RecordFold(ctx context.Context, personID int64, ruleHash string) error {
	s.folded = append(s.folded, personID)
	return nil
}

// differentJudge reads every pair as two occurrences.
type differentJudge struct{ asked int }

func (j *differentJudge) IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error) {
	j.asked++
	return OccurrenceDifferent, nil
}

// The fold sweep starts no person after its gate budget and memoizes the
// persons it folded; the persons left stay pending.
func TestRunFoldSweepBudget(t *testing.T) {
	store := &sweepFoldStorer{foldClusterStorer: &foldClusterStorer{facts: []FactForFold{
		{ID: 1, Text: "A", Embedding: []float32{1, 0, 0}},
		{ID: 2, Text: "B", Embedding: []float32{1, 0, 0}},
	}}}
	judge := &differentJudge{}
	stats, err := RunFoldSweep(context.Background(), store, judge, FoldOptions{Floor: 0.70, MaxCluster: 12}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Persons != 1 || stats.Asked != 1 || judge.asked != 1 || len(store.folded) != 1 || store.folded[0] != 1 {
		t.Errorf("stats=%+v asked=%d folded=%v; want person 1 folded within the budget, person 2 left", stats, judge.asked, store.folded)
	}
}
