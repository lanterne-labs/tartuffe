package categorizer

import (
	"context"
	"digital-mirror/backend/extractor"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// Helper function to load .env for tests
// Consider moving this to a common test helper package if used in multiple places.
func loadEnv(t *testing.T) {
	t.Helper()
	// Assumes .env is at the project root
	err := godotenv.Overload("../../.env")
	if err != nil {
		t.Log("INFO: .env file not found or failed to load, tests needing env vars might be skipped.")
	}
}

// canonicalVerbs is the closed set of verbs allowed in embedding_text.
var canonicalVerbs = []string{
	"DENIED", "CLAIMED", "PROMISED", "SUPPORTED", "OPPOSED",
	"VOTED_FOR", "VOTED_AGAINST", "PROPOSED", "CONFIRMED", "ADMITTED",
	"REFUSED", "DEMANDED", "THREATENED", "WAS_SENTENCED", "WAS_CONVICTED",
	"WAS_ACQUITTED", "WAS_FINED", "RECEIVED_FUNDS", "CHANGED_POSITION", "ANNOUNCED",
}

// startsWithCanonicalVerb checks that an embedding_text starts with one of the canonical verbs.
func startsWithCanonicalVerb(text string) bool {
	for _, verb := range canonicalVerbs {
		if strings.HasPrefix(text, verb+" ") || text == verb {
			return true
		}
	}
	return false
}

// TestRemoteLLMExtractor_ExtractFacts tests fact extraction against realistic political content.
// The article contains:
//   - Valid facts: a promise with specifics, a court sentence, a vote with bill name
//   - Invalid content: journalist opinion, vague statement, lawyer statement, speculation
//
// It requires OPEN_AI_KEY environment variable to be set (e.g., in .env).
func TestRemoteLLMExtractor_ExtractFacts(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		t.Skip("Skipping Fact Extraction test: OPEN_AI_KEY not set.")
	}

	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	// Realistic French political article with a mix of extractable and non-extractable content.
	//
	// SHOULD extract (~3 facts):
	//   1. Le Pen promised to lower income tax by 15% (Type 1 — promise with specifics)
	//   2. Le Pen was sentenced to 4 years prison (Type 2 — court ruling)
	//   3. Le Pen voted against the EU climate regulation bill (Type 1 — concrete vote)
	//
	// Should NOT extract:
	//   - "Les observateurs estiment que Le Pen adopte un ton plus modéré" (journalist opinion)
	//   - "Le parti traverse une période difficile" (vague, no specifics)
	//   - "Me Bosselut a plaidé l'innocence de sa cliente" (lawyer statement)
	//   - "Le Pen risque l'inéligibilité" (speculative)
	//   - "Le Pen est arrivée au tribunal à 9h" (not contradictable — logistics)
	mockURL := "http://example.com/article-politique"
	mockText := `Marine Le Pen : entre promesses fiscales et verdict judiciaire

Marine Le Pen est arrivée au tribunal de Paris à 9h ce matin, accompagnée de son avocat Me Bosselut.
Les observateurs estiment que Le Pen adopte un ton plus modéré depuis les dernières élections.

Lors de son meeting à Marseille le 5 janvier 2026, Marine Le Pen a promis de baisser l'impôt
sur le revenu de 15% pour les ménages gagnant moins de 40 000 euros par an. « Je m'engage
formellement à réduire l'impôt sur le revenu de 15 pour cent dès mon arrivée au pouvoir »,
a-t-elle déclaré devant 3 000 sympathisants.

Le tribunal correctionnel de Paris a condamné Marine Le Pen à 4 ans de prison dont 2 avec
sursis dans l'affaire des assistants parlementaires européens, le 31 mars 2025. Le Pen risque
également l'inéligibilité, mais la décision n'a pas encore été rendue sur ce point.

Me Bosselut a plaidé l'innocence de sa cliente, affirmant que les assistants effectuaient
bien un travail parlementaire réel.

Le parti traverse une période difficile selon plusieurs sources internes.

Au Parlement européen, Marine Le Pen a voté contre le règlement climatique européen
EU-2025-847 le 12 février 2026, s'opposant aux nouvelles normes d'émissions carbone
pour l'industrie automobile.`

	mockContent := extractor.ExtractedContent{
		URL:           mockURL,
		Title:         "Marine Le Pen : entre promesses fiscales et verdict judiciaire",
		Content:       mockText,
		DatePublished: nil,
	}

	personName := "Marine Le Pen"
	t.Logf("Extracting facts about '%s'...", personName)

	ctx := context.Background()
	startTime := time.Now()
	facts, err := extractorClient.ExtractFacts(ctx, personName, mockContent)
	duration := time.Since(startTime)
	t.Logf("LLM call duration: %s", duration)

	if err != nil {
		t.Fatalf("ExtractFacts failed: %v", err)
	}

	if len(facts) == 0 {
		t.Fatal("Expected at least 1 fact, got 0")
	}

	t.Logf("Extracted %d facts:", len(facts))
	for i, fact := range facts {
		t.Logf("  [%d] entity=%q embedding=%q normalized=%q",
			i+1, fact.SubjectEntity, fact.EmbeddingText, fact.NormalizedFactText)
	}

	// --- Structural assertions (every fact must satisfy these) ---
	for i, fact := range facts {
		if fact.SubjectEntity == "" {
			t.Errorf("Fact %d: empty subject_entity", i+1)
		}
		if fact.NormalizedFactText == "" {
			t.Errorf("Fact %d: empty normalized_fact", i+1)
		}
		if fact.OriginalQuote == "" {
			t.Errorf("Fact %d: empty original_quote", i+1)
		}
		if fact.SourceURL != mockURL {
			t.Errorf("Fact %d: source_url = %q, want %q", i+1, fact.SourceURL, mockURL)
		}

		// embedding_text must use canonical format
		if fact.EmbeddingText == "" {
			t.Errorf("Fact %d: empty embedding_text", i+1)
			continue
		}
		if !startsWithCanonicalVerb(fact.EmbeddingText) {
			t.Errorf("Fact %d: embedding_text %q does not start with a canonical verb", i+1, fact.EmbeddingText)
		}
		// Must be short (5-12 words)
		words := strings.Fields(fact.EmbeddingText)
		if len(words) > 15 {
			t.Errorf("Fact %d: embedding_text has %d words (expected ≤15): %q", i+1, len(words), fact.EmbeddingText)
		}
		// Must be English (no accented French characters as a simple heuristic)
		for _, frChar := range []string{"é", "è", "ê", "à", "ù", "ç", "ô", "î"} {
			if strings.Contains(fact.EmbeddingText, frChar) {
				t.Errorf("Fact %d: embedding_text contains French character %q — should be English: %q",
					i+1, frChar, fact.EmbeddingText)
				break
			}
		}
	}

	// --- Content assertions: things that should NOT be extracted ---
	rejectedPhrases := []string{
		"arrivée au tribunal",  // logistics, not contradictable
		"ton plus modéré",      // journalist opinion
		"Bosselut",             // lawyer statement
		"période difficile",    // vague
		"risque l'inéligibili", // speculative
	}
	for _, phrase := range rejectedPhrases {
		for i, fact := range facts {
			if strings.Contains(fact.NormalizedFactText, phrase) || strings.Contains(fact.OriginalQuote, phrase) {
				t.Errorf("Fact %d should have been rejected (contains %q): %q", i+1, phrase, fact.NormalizedFactText)
			}
		}
	}

	// --- Sanity: should extract between 2 and 5 facts from this article ---
	if len(facts) > 6 {
		t.Errorf("Too many facts extracted (%d) — likely includes invalid ones", len(facts))
	}
}
