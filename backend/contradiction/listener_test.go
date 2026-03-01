package contradiction

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/ingester"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
)

// fakeEmbedder returns identical vectors for all inputs to force neighbors within threshold.
type fakeEmbedder struct{}

func (f *fakeEmbedder) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 1536)
		// all zeros is fine for distance = 0
		out[i] = v
	}
	return out, nil
}

// fakeDetector flags contradictions when one mentions "supports tax cuts" and the other "opposes tax cuts".
// For simplicity, this fake detector ignores dates - a real implementation would consider temporal ordering.
type fakeDetector struct{}

func (f *fakeDetector) AreContradictory(ctx context.Context, a string, dateA *time.Time, b string, dateB *time.Time) (bool, string, error) {
	as := containsAllFold(a, []string{"supports", "tax", "cuts"})
	ao := containsAllFold(a, []string{"opposes", "tax", "cuts"})
	bs := containsAllFold(b, []string{"supports", "tax", "cuts"})
	bo := containsAllFold(b, []string{"opposes", "tax", "cuts"})
	if (as && bo) || (ao && bs) {
		return true, "Opposite stances on tax cuts", nil
	}
	return false, "", nil
}

func containsAllFold(s string, subs []string) bool {
	ls := strings.ToLower(s)
	for _, sub := range subs {
		if !strings.Contains(ls, sub) {
			return false
		}
	}
	return true
}

func TestEndToEnd_ArtificialFacts_Contradictions(t *testing.T) {
	t.Helper()
	_ = godotenv.Overload("../../.env")
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping end-to-end contradiction test")
	}

	baseCtx := context.Background()
	ctx, cancel := context.WithTimeout(baseCtx, 2*time.Second)
	defer cancel()
	st, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		t.Skipf("Skipping end-to-end test: %v", err)
		return
	}
	defer st.Close()

	// Clean tables
	_, err = st.Pool().Exec(ctx, "TRUNCATE TABLE contradictions, fact_mentions, fact_sources, facts, persons RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	ing := &ingester.FactIngester{Embedder: &fakeEmbedder{}, Store: st}

	// Prepare three facts for the same person; two are contradictory, one unrelated.
	person := "Alice Doe"
	now := time.Now().UTC()
	inputs := []storage.StoreFactInput{
		{
			SubjectEntity:      person,
			NormalizedFactText: "Alice Doe supports tax cuts in 2024",
			OriginalQuote:      "supports tax cuts",
			CanonicalVerb:      "SUPPORTED",
			SourceURL:          "http://example.com/a1",
			PublishedAt:        &now,
		},
		{
			SubjectEntity:      person,
			NormalizedFactText: "Alice Doe opposes tax cuts in 2024",
			OriginalQuote:      "opposes tax cuts",
			CanonicalVerb:      "OPPOSED",
			SourceURL:          "http://example.com/a2",
			PublishedAt:        &now,
		},
		{
			SubjectEntity:      person,
			NormalizedFactText: "Alice Doe visited Paris in 2023",
			OriginalQuote:      "visited Paris",
			CanonicalVerb:      "CLAIMED",
			SourceURL:          "http://example.com/a3",
			PublishedAt:        &now,
		},
	}

	// Ingest (embeds via fake embedder and stores via storage)
	err = ing.Ingest(ctx, inputs)
	require.NoError(t, err)

	// Build listener with fake detector
	lis := &Listener{Pool: st.Pool(), Storer: st, Detector: &fakeDetector{}}

	// Find the ID of the second fact (opposes) and process it as the new fact
	var opposesID int64
	err = st.Pool().QueryRow(ctx, `SELECT id FROM facts WHERE fact_text = $1`, "Alice Doe opposes tax cuts in 2024").Scan(&opposesID)
	require.NoError(t, err)

	err = lis.processFact(ctx, opposesID)
	require.NoError(t, err)

	// Verify one contradiction recorded for Alice Doe between the supports and opposes facts
	var personID int64
	err = st.Pool().QueryRow(ctx, `SELECT id FROM persons WHERE name = $1`, strings.ToLower(person)).Scan(&personID)
	require.NoError(t, err)

	var count int
	err = st.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM contradictions WHERE person_id = $1`, personID).Scan(&count)
	require.NoError(t, err)
	if count != 1 {
		t.Fatalf("expected 1 contradiction, got %d", count)
	}
}
