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

func mockCosineDistance(a, b []float32) float64 {
	if len(a) != len(b) {
		return 2.0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 2.0
	}
	return 1.0 - dot/(math.Sqrt(na)*math.Sqrt(nb))
}

func (m *mockStanceStorer) FindMatchingStance(ctx context.Context, personID int64, embedding []float32, threshold float64) (*StanceMatch, error) {
	var best *StanceMatch
	for _, s := range m.stances {
		if s.PersonID != personID {
			continue
		}
		dist := mockCosineDistance(embedding, s.ClusterEmbedding)
		if dist > threshold {
			continue
		}
		if best == nil || dist < best.Distance {
			best = &StanceMatch{
				StanceDocument: s,
				Distance:       dist,
			}
		}
	}
	return best, nil
}

func (m *mockStanceStorer) InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error) {
	id := m.nextID
	m.nextID++
	doc.ID = id
	m.stances[id] = doc
	return id, nil
}

func (m *mockStanceStorer) UpdateStanceDocument(ctx context.Context, id int64, summary string, embedding []float32, keyFactIDs []int64, factCount int) error {
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

func (m *mockStanceStorer) RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason string) error {
	m.contradictions = append(m.contradictions, mockContradiction{
		personID: personID,
		factAID:  factAID,
		factBID:  factBID,
		reason:   reason,
	})
	return nil
}

func TestProcessNewFact_NewStance(t *testing.T) {
	mock := newMockStanceStorer()
	sm := &StanceManager{
		storer:    mock,
		threshold: 0.35,
	}

	embedding := []float32{1, 0, 0}
	result, err := sm.ProcessNewFact(context.Background(), 42, 100, "Marine Le Pen denied wrongdoing", embedding, "DENIED")
	if err != nil {
		t.Fatalf("ProcessNewFact returned error: %v", err)
	}

	if result.Verdict != "NEW_STANCE" {
		t.Errorf("expected verdict NEW_STANCE, got %q", result.Verdict)
	}

	if result.StanceID == 0 {
		t.Error("expected non-zero StanceID")
	}

	// Verify a stance document was inserted.
	if len(mock.stances) != 1 {
		t.Errorf("expected 1 stance document, got %d", len(mock.stances))
	}

	doc := mock.stances[result.StanceID]
	if doc.PersonID != 42 {
		t.Errorf("expected PersonID 42, got %d", doc.PersonID)
	}
	if doc.FactCount != 1 {
		t.Errorf("expected FactCount 1, got %d", doc.FactCount)
	}
	if len(doc.KeyFactIDs) != 1 || doc.KeyFactIDs[0] != 100 {
		t.Errorf("expected KeyFactIDs [100], got %v", doc.KeyFactIDs)
	}

	// Verify the fact was added to the stance.
	facts := mock.facts[result.StanceID]
	if len(facts) != 1 {
		t.Errorf("expected 1 fact linked to stance, got %d", len(facts))
	}
	if facts[0].ID != 100 {
		t.Errorf("expected linked fact ID 100, got %d", facts[0].ID)
	}
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
