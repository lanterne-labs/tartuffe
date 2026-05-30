package eval

import (
	"testing"
)

// TestEvaluate_BasicCases covers TP / FN / FP / TN counting and the unexpected-
// pair handling. The corpus has 4 facts (a,b,c,d) and 3 expected pairs:
//
//	(a,b)=YES   (a,c)=NO   (b,c)=YES
//
// Detector flags (a,b) and (c,d).
//
//	(a,b) → expected YES, detected → TP
//	(a,c) → expected NO,  not detected → TN
//	(b,c) → expected YES, not detected → FN
//	(c,d) → not in expected_pairs, detected → FP (Unexpected=true)
func TestEvaluate_BasicCases(t *testing.T) {
	corpus := &Corpus{
		Persons: []Person{{
			Name: "Test Person", EntityType: "human",
			Stances: []Stance{{
				Axis: "test",
				Facts: []Fact{
					{ID: "a", Text: "fact a", Verb: "CLAIMED", EmbeddingText: "x", SourceURL: "http://t/a"},
					{ID: "b", Text: "fact b", Verb: "CLAIMED", EmbeddingText: "x", SourceURL: "http://t/b"},
					{ID: "c", Text: "fact c", Verb: "CLAIMED", EmbeddingText: "x", SourceURL: "http://t/c"},
					{ID: "d", Text: "fact d", Verb: "CLAIMED", EmbeddingText: "x", SourceURL: "http://t/d"},
				},
			}},
		}},
		ExpectedPairs: []PairSpec{
			{A: "a", B: "b", Expected: "YES", Rule: "direct"},
			{A: "a", B: "c", Expected: "NO", Rule: "noise"},
			{A: "b", B: "c", Expected: "YES", Rule: "values"},
		},
	}
	if err := corpus.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	idMap := map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4}
	detected := []DetectedContradiction{
		{FactAID: 1, FactBID: 2, Reason: "found ab"},
		{FactAID: 3, FactBID: 4, Reason: "spurious cd"},
	}

	rep := Evaluate(corpus, idMap, detected)

	if rep.TruePos != 1 {
		t.Errorf("TruePos = %d, want 1", rep.TruePos)
	}
	if rep.FalseNeg != 1 {
		t.Errorf("FalseNeg = %d, want 1", rep.FalseNeg)
	}
	if rep.FalsePos != 1 {
		t.Errorf("FalsePos = %d, want 1", rep.FalsePos)
	}
	if rep.TrueNeg != 1 {
		t.Errorf("TrueNeg = %d, want 1", rep.TrueNeg)
	}
	if rep.UnexpectedF != 1 {
		t.Errorf("UnexpectedF = %d, want 1", rep.UnexpectedF)
	}

	// Precision = TP / (TP + FP) = 1/2 = 0.5
	if got := rep.Precision(); got != 0.5 {
		t.Errorf("Precision = %v, want 0.5", got)
	}
	// Recall = TP / (TP + FN) = 1/2 = 0.5
	if got := rep.Recall(); got != 0.5 {
		t.Errorf("Recall = %v, want 0.5", got)
	}
}

// TestEvaluate_PerfectScore: detector flags exactly what's expected.
func TestEvaluate_PerfectScore(t *testing.T) {
	corpus := &Corpus{
		Persons: []Person{{
			Name: "T", EntityType: "human",
			Stances: []Stance{{Axis: "t", Facts: []Fact{
				{ID: "x", Text: "x", Verb: "V", EmbeddingText: "e", SourceURL: "http://t/x"},
				{ID: "y", Text: "y", Verb: "V", EmbeddingText: "e", SourceURL: "http://t/y"},
			}}},
		}},
		ExpectedPairs: []PairSpec{
			{A: "x", B: "y", Expected: "YES", Rule: "r"},
		},
	}
	if err := corpus.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	rep := Evaluate(corpus, map[string]int64{"x": 1, "y": 2}, []DetectedContradiction{{FactAID: 1, FactBID: 2}})
	if rep.Precision() != 1.0 || rep.Recall() != 1.0 {
		t.Errorf("expected perfect P/R, got P=%v R=%v", rep.Precision(), rep.Recall())
	}
}

// TestPairKey: order-insensitive canonical key.
func TestPairKey_OrderInsensitive(t *testing.T) {
	k1 := PairKey(5, 3)
	k2 := PairKey(3, 5)
	if k1 != k2 {
		t.Errorf("PairKey not order-insensitive: %v != %v", k1, k2)
	}
	if k1.A != 3 || k1.B != 5 {
		t.Errorf("PairKey not canonicalised: %+v", k1)
	}
}

// TestEvaluate_MappingMissing: a corpus pair whose IDs didn't make it into
// idMap (e.g. the fact was deduped at insertion) is recorded as
// MAPPING_MISSING and excluded from stats counters.
func TestEvaluate_MappingMissing(t *testing.T) {
	corpus := &Corpus{
		Persons: []Person{{
			Name: "T", EntityType: "human",
			Stances: []Stance{{Axis: "t", Facts: []Fact{
				{ID: "x", Text: "x", Verb: "V", EmbeddingText: "e", SourceURL: "http://t/x"},
				{ID: "y", Text: "y", Verb: "V", EmbeddingText: "e", SourceURL: "http://t/y"},
			}}},
		}},
		ExpectedPairs: []PairSpec{{A: "x", B: "y", Expected: "YES", Rule: "r"}},
	}
	if err := corpus.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// y is missing from the map (deduped).
	rep := Evaluate(corpus, map[string]int64{"x": 1}, nil)
	if rep.TruePos+rep.FalseNeg+rep.FalsePos+rep.TrueNeg != 0 {
		t.Errorf("expected 0 counted outcomes when mapping is missing; got TP=%d FN=%d FP=%d TN=%d",
			rep.TruePos, rep.FalseNeg, rep.FalsePos, rep.TrueNeg)
	}
	var mm int
	for _, o := range rep.Outcomes {
		if o.Outcome == "MAPPING_MISSING" {
			mm++
		}
	}
	if mm != 1 {
		t.Errorf("expected 1 MAPPING_MISSING outcome, got %d", mm)
	}
}
