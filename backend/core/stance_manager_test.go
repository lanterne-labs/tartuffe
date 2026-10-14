package core

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// mockStanceStorer implements StanceStorer for testing.
type mockStanceStorer struct {
	stances        map[int64]StanceDocument   // keyed by ID
	facts          map[int64][]StanceFactInfo // keyed by stance ID
	nextID         int64
	contradictions []mockContradiction
	unsummarized   []StanceFactInfo
	tensions       map[int64][]StanceTension
	consumed       [][]int64 // fact ids passed to UpdateStanceMetadata, per call
}

type mockContradiction struct {
	personID int64
	factAID  int64
	factBID  int64
	reason   string
}

func newMockStanceStorer() *mockStanceStorer {
	return &mockStanceStorer{
		stances: make(map[int64]StanceDocument),
		facts:   make(map[int64][]StanceFactInfo),
		nextID:  1,
	}
}

func (m *mockStanceStorer) InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error) {
	id := m.nextID
	m.nextID++
	doc.ID = id
	m.stances[id] = doc
	return id, nil
}

func (m *mockStanceStorer) UpdateStanceDocument(ctx context.Context, id int64, summary, summaryEn string, embedding []float32, keyFactIDs []int64, factCount int) error {
	doc, ok := m.stances[id]
	if !ok {
		return nil
	}
	doc.Summary = summary
	doc.ClusterEmbedding = embedding
	doc.KeyFactIDs = keyFactIDs
	doc.FactCount = factCount
	m.stances[id] = doc
	return nil
}

func (m *mockStanceStorer) UpdateStanceMetadata(ctx context.Context, id int64, label, summary string, consumedFactIDs []int64) error {
	doc, ok := m.stances[id]
	if !ok {
		return nil
	}
	doc.ClusterLabel = label
	doc.Summary = summary
	m.stances[id] = doc
	m.consumed = append(m.consumed, consumedFactIDs)
	return nil
}

func (m *mockStanceStorer) GetStanceTensions(ctx context.Context, stanceID int64) (map[int64][]StanceTension, error) {
	return m.tensions, nil
}

func (m *mockStanceStorer) AddFactToStance(ctx context.Context, stanceID int64, factID int64) error {
	m.facts[stanceID] = append(m.facts[stanceID], StanceFactInfo{ID: factID})
	return nil
}

func (m *mockStanceStorer) GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error) {
	var result []StanceDocument
	for _, s := range m.stances {
		if s.PersonID == personID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockStanceStorer) GetStanceDocument(ctx context.Context, id int64) (StanceDocument, error) {
	doc, ok := m.stances[id]
	if !ok {
		return StanceDocument{}, fmt.Errorf("stance %d not found", id)
	}
	return doc, nil
}

func (m *mockStanceStorer) StanceCandidatesByIDs(ctx context.Context, personID int64, factEmbedding []float32, stanceIDs []int64, factsPerStance int, excludeFactID int64) ([]StanceCandidate, error) {
	var result []StanceCandidate
	for _, id := range stanceIDs {
		for _, s := range m.stances {
			if s.ID == id && s.PersonID == personID {
				result = append(result, StanceCandidate{ID: s.ID, Label: s.ClusterLabel, Summary: s.Summary, FactCount: s.FactCount})
			}
		}
	}
	return result, nil
}

func (m *mockStanceStorer) ShortlistStancesByNearestFact(ctx context.Context, personID int64, factEmbedding []float32, topK, factsPerStance int, excludeFactID int64) ([]StanceCandidate, error) {
	var result []StanceCandidate
	for _, s := range m.stances {
		if s.PersonID != personID {
			continue
		}
		result = append(result, StanceCandidate{
			ID:        s.ID,
			Label:     s.ClusterLabel,
			Summary:   s.Summary,
			FactCount: s.FactCount,
		})
		if len(result) >= topK {
			break
		}
	}
	return result, nil
}

func (m *mockStanceStorer) GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error) {
	return m.facts[stanceID], nil
}

// unsummarized, when set, is what GetUnsummarizedFactsForStance returns for
// every stance; nil means "all facts are new" (never summarized).
func (m *mockStanceStorer) GetUnsummarizedFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error) {
	if m.unsummarized != nil {
		return m.unsummarized, nil
	}
	return m.facts[stanceID], nil
}

func (m *mockStanceStorer) GetStanceMetadata(ctx context.Context, stanceID int64) (string, string, error) {
	doc, ok := m.stances[stanceID]
	if !ok {
		return "", "", nil
	}
	return doc.ClusterLabel, doc.Summary, nil
}

func (m *mockStanceStorer) RecordContradictionImmediate(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) (int64, bool, error) {
	m.contradictions = append(m.contradictions, mockContradiction{
		personID: personID,
		factAID:  factAID,
		factBID:  factBID,
		reason:   reason,
	})
	return int64(len(m.contradictions)), false, nil
}

// TestProcessNewFact_NewStance previously verified the "no existing stances"
// shortcut that used truncate(factText, 120) as the stance label. That shortcut
// has been removed: ProcessNewFact now ALWAYS routes through assignTopic, which
// makes a real LLM call. Without an LLM-mock injection point on StanceManager,
// this test would hit the network. Skipped until we add an injectable client
// (or convert it to an integration test).
func TestProcessNewFact_NewStance(t *testing.T) {
	t.Skip("ProcessNewFact now always calls the LLM (no factText shortcut); needs LLM-client injection to test in unit form")
}

// TestRecordStanceContradictions covers the dispatch / skip logic shared by
// handleVerdict (live ingest) and applyMergeSubgroup (post-merge sweep).
// Verifies invalid entries (KeyFactID <= 0) and self-pairs (KeyFactID == factID)
// are dropped, and that valid entries reach the storer.
func TestRecordStanceContradictions(t *testing.T) {
	mk := func() (*StanceManager, *mockStanceStorer) {
		mock := newMockStanceStorer()
		return &StanceManager{storer: mock}, mock
	}

	t.Run("empty list records nothing", func(t *testing.T) {
		sm, mock := mk()
		got := sm.recordStanceContradictions(context.Background(), 42, 100, 7, nil)
		if got != 0 {
			t.Errorf("expected 0 recorded, got %d", got)
		}
		if len(mock.contradictions) != 0 {
			t.Errorf("expected no DB writes, got %d", len(mock.contradictions))
		}
	})

	t.Run("skips KeyFactID <= 0", func(t *testing.T) {
		sm, mock := mk()
		entries := []StanceContradictionEntry{
			{KeyFactID: 0, Reason: "missing id"},
			{KeyFactID: -3, Reason: "negative id"},
		}
		got := sm.recordStanceContradictions(context.Background(), 42, 100, 7, entries)
		if got != 0 {
			t.Errorf("expected 0 recorded, got %d", got)
		}
		if len(mock.contradictions) != 0 {
			t.Errorf("expected no DB writes, got %d", len(mock.contradictions))
		}
	})

	t.Run("skips self-pair", func(t *testing.T) {
		sm, mock := mk()
		entries := []StanceContradictionEntry{
			{KeyFactID: 100, Reason: "self-pair (factID == 100)"},
		}
		got := sm.recordStanceContradictions(context.Background(), 42, 100, 7, entries)
		if got != 0 {
			t.Errorf("expected 0 recorded, got %d", got)
		}
		if len(mock.contradictions) != 0 {
			t.Errorf("expected no DB writes, got %d", len(mock.contradictions))
		}
	})

	t.Run("records valid entries and skips invalid in same call", func(t *testing.T) {
		sm, mock := mk()
		entries := []StanceContradictionEntry{
			{KeyFactID: 200, Reason: "valid one"},
			{KeyFactID: 100, Reason: "self-pair, drop"},
			{KeyFactID: 0, Reason: "zero, drop"},
			{KeyFactID: 300, Reason: "valid two"},
		}
		got := sm.recordStanceContradictions(context.Background(), 42, 100, 7, entries)
		if got != 2 {
			t.Errorf("expected 2 recorded, got %d", got)
		}
		if len(mock.contradictions) != 2 {
			t.Fatalf("expected 2 DB writes, got %d", len(mock.contradictions))
		}
		if mock.contradictions[0].factBID != 200 || mock.contradictions[1].factBID != 300 {
			t.Errorf("unexpected contradictions: %+v", mock.contradictions)
		}
		for _, c := range mock.contradictions {
			if c.personID != 42 || c.factAID != 100 {
				t.Errorf("unexpected ids on contradiction: %+v", c)
			}
		}
	})
}

// TestChunkKeyFacts verifies that the analyze-batch chunker splits long key-fact
// lists into batches of at most keyFactBatchSize, and handles the edge cases.
func TestChunkKeyFactsByBudget(t *testing.T) {
	const summary = "The person's stance on pension reform and the legal retirement age."
	const newFact = "Jordan Bardella a suggéré de supprimer tout âge légal de départ à la retraite."

	mkFacts := func(n int, text string) []StanceFactInfo {
		out := make([]StanceFactInfo, n)
		for i := range out {
			out[i] = StanceFactInfo{ID: int64(i + 1), CanonicalVerb: "CLAIMED", NormalizedFactText: text}
		}
		return out
	}

	// The invariant the fix guarantees for every batch.
	assertFits := func(t *testing.T, batches [][]StanceFactInfo) {
		t.Helper()
		base := estimateStanceBaseTokens(summary, newFact)
		for i, b := range batches {
			if len(b) == 0 {
				t.Fatalf("batch %d is empty", i)
			}
			factTokens := 0
			for _, f := range b {
				factTokens += estimateStanceTokens(formatKeyFactLine(f))
			}
			total := base + factTokens + stanceAnalyzeOutputTokens + stanceContextSafetyTokens
			// Allow the single-fact degenerate case to exceed (nothing batching can
			// do when one fact + base already overflows); otherwise must fit.
			if total > stanceMaxContextTokens && len(b) > 1 {
				t.Errorf("batch %d overflows: %d > %d (base=%d facts=%d)", i, total, stanceMaxContextTokens, base, factTokens)
			}
		}
	}

	t.Run("empty returns nil", func(t *testing.T) {
		if got := chunkKeyFactsByBudget(summary, newFact, nil); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("a few short facts fit in one batch", func(t *testing.T) {
		got := chunkKeyFactsByBudget(summary, newFact, mkFacts(5, "short fact text"))
		if len(got) != 1 || len(got[0]) != 5 {
			t.Errorf("expected 1 batch of 5, got %v", batchSizes(got))
		}
		assertFits(t, got)
	})

	t.Run("many long facts split into multiple fitting batches", func(t *testing.T) {
		long := "Jordan Bardella a déclaré que l'autorité de l'État doit être assurée partout face aux violences et dégradations qui se multiplient dans plusieurs villes françaises."
		got := chunkKeyFactsByBudget(summary, newFact, mkFacts(120, long))
		if len(got) < 2 {
			t.Fatalf("expected multiple batches for 120 long facts, got %d", len(got))
		}
		assertFits(t, got)
		// All facts preserved, in order.
		total, prevID := 0, int64(0)
		for _, b := range got {
			for _, f := range b {
				total++
				if f.ID <= prevID {
					t.Errorf("facts out of order: %d after %d", f.ID, prevID)
				}
				prevID = f.ID
			}
		}
		if total != 120 {
			t.Errorf("expected 120 facts preserved, got %d", total)
		}
	})
}

func batchSizes(batches [][]StanceFactInfo) []int {
	out := make([]int, len(batches))
	for i, b := range batches {
		out[i] = len(b)
	}
	return out
}

func slicesEqualInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseStanceLLMResponse covers the new multi-verdict response schema.
func TestParseStanceLLMResponse(t *testing.T) {
	t.Run("valid CONTRADICTION with two entries", func(t *testing.T) {
		raw := `{
			"reasoning": "...",
			"primary_verdict": "CONTRADICTION",
			"duplicate_of_key_fact_id": 0,
			"contradictions": [
				{"key_fact_id": 42, "reason": "first"},
				{"key_fact_id": 99, "reason": "second"}
			]
		}`
		resp, err := parseStanceLLMResponse(raw)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if resp.PrimaryVerdict != "CONTRADICTION" || len(resp.Contradictions) != 2 {
			t.Errorf("unexpected response: %+v", resp)
		}
	})

	t.Run("CONTRADICTION with empty list downgrades to NEW_INFO", func(t *testing.T) {
		raw := `{"reasoning": "...", "primary_verdict": "CONTRADICTION", "duplicate_of_key_fact_id": 0, "contradictions": []}`
		resp, err := parseStanceLLMResponse(raw)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if resp.PrimaryVerdict != "NEW_INFO" {
			t.Errorf("expected downgrade to NEW_INFO, got %q", resp.PrimaryVerdict)
		}
	})

	t.Run("DUPLICATE with duplicate_of", func(t *testing.T) {
		raw := `{"reasoning": "...", "primary_verdict": "DUPLICATE", "duplicate_of_key_fact_id": 7, "contradictions": []}`
		resp, err := parseStanceLLMResponse(raw)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if resp.DuplicateOf != 7 {
			t.Errorf("expected DuplicateOf=7, got %d", resp.DuplicateOf)
		}
	})

	t.Run("invalid verdict errors", func(t *testing.T) {
		raw := `{"primary_verdict": "MAYBE", "contradictions": []}`
		if _, err := parseStanceLLMResponse(raw); err == nil {
			t.Error("expected error for invalid verdict")
		}
	})

	t.Run("markdown-wrapped JSON", func(t *testing.T) {
		raw := "```json\n{\"primary_verdict\": \"NEW_INFO\", \"contradictions\": []}\n```"
		resp, err := parseStanceLLMResponse(raw)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if resp.PrimaryVerdict != "NEW_INFO" {
			t.Errorf("expected NEW_INFO, got %q", resp.PrimaryVerdict)
		}
	})
}

func TestUpdateCentroid(t *testing.T) {
	t.Run("equal weight", func(t *testing.T) {
		old := []float32{1, 0, 0}
		newEmb := []float32{0, 1, 0}
		result := updateCentroid(old, newEmb, 1)

		// With oldCount=1, weighted average is ((1,0,0)*1 + (0,1,0)) / 2 = (0.5, 0.5, 0)
		// Normalized: (0.5/norm, 0.5/norm, 0) where norm = sqrt(0.25+0.25) = sqrt(0.5) ≈ 0.7071
		// So each component ≈ 0.7071
		expected := float32(1.0 / math.Sqrt(2.0))
		tol := float32(0.001)

		if abs32(result[0]-expected) > tol {
			t.Errorf("result[0] = %f, expected ~%f", result[0], expected)
		}
		if abs32(result[1]-expected) > tol {
			t.Errorf("result[1] = %f, expected ~%f", result[1], expected)
		}
		if abs32(result[2]) > tol {
			t.Errorf("result[2] = %f, expected ~0", result[2])
		}
	})

	t.Run("same direction", func(t *testing.T) {
		old := []float32{1, 0, 0}
		newEmb := []float32{1, 0, 0}
		result := updateCentroid(old, newEmb, 1)

		// Average of two identical vectors normalized stays the same.
		tol := float32(0.001)
		if abs32(result[0]-1.0) > tol {
			t.Errorf("result[0] = %f, expected ~1.0", result[0])
		}
		if abs32(result[1]) > tol {
			t.Errorf("result[1] = %f, expected ~0", result[1])
		}
		if abs32(result[2]) > tol {
			t.Errorf("result[2] = %f, expected ~0", result[2])
		}
	})
}

func TestUpdateCentroid_Weighted(t *testing.T) {
	old := []float32{1, 0, 0}
	newEmb := []float32{0, 1, 0}
	result := updateCentroid(old, newEmb, 3)

	// With oldCount=3, weighted average is ((1,0,0)*3 + (0,1,0)) / 4 = (0.75, 0.25, 0)
	// Normalized: norm = sqrt(0.75^2 + 0.25^2) = sqrt(0.5625 + 0.0625) = sqrt(0.625)
	// result[0] = 0.75/sqrt(0.625) ≈ 0.9487
	// result[1] = 0.25/sqrt(0.625) ≈ 0.3162
	norm := math.Sqrt(0.75*0.75 + 0.25*0.25)
	expectedX := float32(0.75 / norm)
	expectedY := float32(0.25 / norm)
	tol := float32(0.001)

	if abs32(result[0]-expectedX) > tol {
		t.Errorf("result[0] = %f, expected ~%f", result[0], expectedX)
	}
	if abs32(result[1]-expectedY) > tol {
		t.Errorf("result[1] = %f, expected ~%f", result[1], expectedY)
	}
	if abs32(result[2]) > tol {
		t.Errorf("result[2] = %f, expected ~0", result[2])
	}

	// The old centroid should have more weight: result[0] should be significantly larger than result[1].
	if result[0] <= result[1] {
		t.Errorf("old centroid should have more weight: result[0]=%f should be > result[1]=%f", result[0], result[1])
	}
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
