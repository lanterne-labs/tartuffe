package core

import (
	"context"
	"testing"
)

// foldClusterStorer is a FactFoldStorer over in-memory facts: no shared
// facts, no contradictions, and a scripted trigram pair list.
type foldClusterStorer struct {
	MetaFoldStorer
	facts     []FactForFold
	textPairs [][2]int64
}

func (f *foldClusterStorer) GetFoldFactsForPerson(ctx context.Context, personID int64) ([]FactForFold, error) {
	return f.facts, nil
}
func (f *foldClusterStorer) ListSharedFactIDs(ctx context.Context, factIDs []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}
func (f *foldClusterStorer) ListContradictionFactPairs(ctx context.Context, personID int64) ([]ContradictionFactPair, error) {
	return nil, nil
}
func (f *foldClusterStorer) ListTextSimilarFactPairs(ctx context.Context, factIDs []int64, floor float64) ([][2]int64, error) {
	return f.textPairs, nil
}
func (f *foldClusterStorer) IsMetaFact(ctx context.Context, factID int64) (bool, error) {
	return false, nil
}
func (f *foldClusterStorer) GetFactEmbeddingMeta(ctx context.Context, factID int64) (string, string, string, error) {
	return "", "fr", "", nil
}

type sameAlwaysJudge struct{ asked [][2]string }

func (j *sameAlwaysJudge) IsSamePosition(ctx context.Context, subject, a, b string) (bool, error) {
	j.asked = append(j.asked, [2]string{a, b})
	return true, nil
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
	if len(clusters) != 1 || len(clusters[0]) != 2 || len(judge.asked) != 1 {
		t.Fatalf("embedding only: clusters=%v asked=%d, want one {A,B} cluster from one gate call", clusters, len(judge.asked))
	}

	store.textPairs = [][2]int64{{1, 3}}
	judge = &sameAlwaysJudge{}
	clusters, err = FoldClustersForPerson(context.Background(), store, judge, p, FoldOptions{Floor: 0.70, TextFloor: 0.40, MaxCluster: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || len(clusters[0]) != 2 || len(judge.asked) != 2 {
		t.Fatalf("with text pairs: clusters=%v asked=%d, want {A,B} only (C never confirmed against B) from two gate calls", clusters, len(judge.asked))
	}
}
