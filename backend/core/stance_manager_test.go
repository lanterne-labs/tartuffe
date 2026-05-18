package core

import (
	"context"
	"math"
	"testing"
)

// mockStanceStorer implements StanceStorer for testing.
type mockStanceStorer struct {
	stances        map[int64]StanceDocument   // keyed by ID
	facts          map[int64][]StanceFactInfo // keyed by stance ID
	nextID         int64
	contradictions []mockContradiction
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

func (m *mockStanceStorer) UpdateStanceMetadata(ctx context.Context, id int64, label, labelEn, summary, summaryEn string) error {
	doc, ok := m.stances[id]
	if !ok {
		return nil
	}
	doc.ClusterLabel = label
	doc.Summary = summary
	m.stances[id] = doc
	return nil
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

func (m *mockStanceStorer) GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error) {
	return m.facts[stanceID], nil
}

func (m *mockStanceStorer) GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error) {
	totalStances = len(m.stances)
	for _, facts := range m.facts {
		totalFactsLinked += len(facts)
	}
	return totalStances, totalFactsLinked, nil
}

func (m *mockStanceStorer) DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error {
	for id, s := range m.stances {
		if s.PersonID == personID {
			delete(m.stances, id)
			delete(m.facts, id)
		}
	}
	return nil
}

func (m *mockStanceStorer) RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) error {
	m.contradictions = append(m.contradictions, mockContradiction{
		personID: personID,
		factAID:  factAID,
		factBID:  factBID,
		reason:   reason,
	})
	return nil
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
// handleVerdict (live ingest) and AnalyzeFactInStance (post-merge sweep).
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
func TestChunkKeyFacts(t *testing.T) {
	mk := func(n int) []StanceFactInfo {
		out := make([]StanceFactInfo, n)
		for i := range out {
			out[i] = StanceFactInfo{ID: int64(i + 1)}
		}
		return out
	}

	t.Run("empty input returns nil", func(t *testing.T) {
		if got := chunkKeyFacts(nil, 10); got != nil {
			t.Errorf("expected nil for empty input, got %v", got)
		}
	})

	t.Run("single batch fits", func(t *testing.T) {
		got := chunkKeyFacts(mk(5), 10)
		if len(got) != 1 || len(got[0]) != 5 {
			t.Errorf("expected 1 batch of 5, got %d batches sized %v", len(got), batchSizes(got))
		}
	})

	t.Run("exact multiple", func(t *testing.T) {
		got := chunkKeyFacts(mk(20), 10)
		if len(got) != 2 || len(got[0]) != 10 || len(got[1]) != 10 {
			t.Errorf("expected 2 batches of 10, got %d batches sized %v", len(got), batchSizes(got))
		}
	})

	t.Run("partial last batch", func(t *testing.T) {
		got := chunkKeyFacts(mk(23), 10)
		if len(got) != 3 || len(got[0]) != 10 || len(got[1]) != 10 || len(got[2]) != 3 {
			t.Errorf("expected batches of [10,10,3], got %v", batchSizes(got))
		}
	})

	t.Run("large batch with default size", func(t *testing.T) {
		got := chunkKeyFacts(mk(105), 0) // 0 → use default keyFactBatchSize
		// With keyFactBatchSize=40: 105/40 = 2 full + 1 partial = 3 batches
		expected := []int{40, 40, 25}
		if got2 := batchSizes(got); !slicesEqualInt(got2, expected) {
			t.Errorf("expected batches %v, got %v", expected, got2)
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

// TestShortlistStancesByEmbedding verifies the embedding-based stance pre-filter
// returns the top-K closest stances by cosine similarity (== dot product since
// embeddings are normalised) and exposes a correct index map back to the
// original stances slice.
func TestShortlistStancesByEmbedding(t *testing.T) {
	stances := []StanceDocument{
		{ID: 1, ClusterEmbedding: []float32{1, 0, 0}},
		{ID: 2, ClusterEmbedding: []float32{0, 1, 0}},
		{ID: 3, ClusterEmbedding: []float32{0.9, 0.1, 0}},
		{ID: 4, ClusterEmbedding: []float32{-1, 0, 0}},
	}
	factEmb := []float32{1, 0, 0}

	t.Run("k >= len returns all without remap", func(t *testing.T) {
		short, idx := shortlistStancesByEmbedding(stances, factEmb, 10)
		if len(short) != 4 {
			t.Errorf("expected all 4 stances back, got %d", len(short))
		}
		if idx != nil {
			t.Errorf("expected nil index map when no filter applied, got %v", idx)
		}
	})

	t.Run("k=2 picks two closest by cosine", func(t *testing.T) {
		short, idx := shortlistStancesByEmbedding(stances, factEmb, 2)
		if len(short) != 2 {
			t.Fatalf("expected 2 stances, got %d", len(short))
		}
		// Closest to (1,0,0): stance 1 (sim 1.0), then stance 3 (sim 0.9).
		if short[0].ID != 1 || short[1].ID != 3 {
			t.Errorf("expected shortlist IDs [1, 3], got [%d, %d]", short[0].ID, short[1].ID)
		}
		if len(idx) != 2 || idx[0] != 0 || idx[1] != 2 {
			t.Errorf("expected index map [0, 2], got %v", idx)
		}
	})

	t.Run("empty embedding returns input unchanged", func(t *testing.T) {
		short, idx := shortlistStancesByEmbedding(stances, nil, 2)
		if len(short) != 4 || idx != nil {
			t.Errorf("expected unchanged input when factEmbedding empty")
		}
	})
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
