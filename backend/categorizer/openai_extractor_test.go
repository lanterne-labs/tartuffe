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
	//   - "la cote de popularité de Le Pen a atteint 35%" (poll/rating — rule 7)
	//   - "Le Pen est invitée à un banquet le 13 juin" (passive event — rule 4)
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

Selon un sondage Elabe publié le 6 juin, la cote de popularité de Marine Le Pen a atteint 35% (+2 points).
Par ailleurs, Marine Le Pen est invitée à un banquet organisé par ses soutiens le 13 juin à Lamontjoie.

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

		// canonical_verb must be set to one of the 20 known verbs
		if fact.CanonicalVerb == "" {
			t.Errorf("Fact %d: empty canonical_verb", i+1)
		} else if !isKnownVerb(fact.CanonicalVerb) {
			t.Errorf("Fact %d: unknown canonical_verb %q", i+1, fact.CanonicalVerb)
		}
		// embedding_text must be verb-free topic
		if fact.EmbeddingText == "" {
			t.Errorf("Fact %d: empty embedding_text", i+1)
			continue
		}
		// embedding_text should NOT start with a canonical verb (verb is now separate)
		if startsWithCanonicalVerb(fact.EmbeddingText) {
			t.Errorf("Fact %d: embedding_text %q still starts with a canonical verb (should be verb-free)", i+1, fact.EmbeddingText)
		}
		// Must be short (2-12 words)
		words := strings.Fields(fact.EmbeddingText)
		if len(words) > 15 {
			t.Errorf("Fact %d: embedding_text has %d words (expected ≤15): %q", i+1, len(words), fact.EmbeddingText)
		}
	}

	// --- Content assertions: things that should NOT be extracted ---
	rejectedPhrases := []string{
		"arrivée au tribunal",  // logistics, not contradictable
		"ton plus modéré",      // journalist opinion
		"Bosselut",             // lawyer statement
		"période difficile",    // vague
		"risque l'inéligibili", // speculative
		"popularité",           // poll/rating snapshot (rule 7)
		"35%",                  // poll number (rule 7) — distinct from the valid "15%" tax fact
		"invitée à un banquet", // passive event the entity didn't act on (rule 4)
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

// TestRemoteLLMExtractor_NoOverSpecification is a live golden test for the grounding
// rule: the extractor must NOT inject specifics absent from the source text. It
// reproduces three real prod over-specification cases — substituting "GIEC"/IPCC for
// a generic "les scientifiques", inventing "le Salvador" as a prison model, and
// inventing an outlet ("Politico"). Each statement below is a legitimately
// extractable position, but deliberately WITHOUT the tempting specific; those
// specifics appear NOWHERE in the text, so a correctly-grounded extractor cannot
// produce them. Requires OPEN_AI_KEY (skips otherwise), like the test above.
func TestRemoteLLMExtractor_NoOverSpecification(t *testing.T) {
	loadEnv(t)
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping over-specification test: OPEN_AI_KEY not set.")
	}

	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	// GIEC and Politico appear here as CONTEXT only — never as part of an entity's
	// own contradictable statement. The grounding rule must keep them out of the
	// facts: Le Pen's fact must say "les scientifiques" (not GIEC), and Bardella's
	// must stay attributed to publicsenat.fr (not Politico). Salvador appears nowhere.
	mockURL := "http://example.com/grounding"
	mockText := `Positions politiques — entretiens et contexte

Contexte de la semaine : un rapport du GIEC et une dépêche de l'agence Politico ont circulé.

Interrogée séparément, Marine Le Pen a qualifié les recommandations des scientifiques sur
le climat de « trop alarmistes ». Elle n'a nommé aucun organisme en particulier.

Dans un entretien accordé à publicsenat.fr, Jordan Bardella a déclaré vouloir bâtir une
nouvelle architecture européenne fondée sur la coopération entre les États.

Éric Ciotti a affirmé qu'il ne faut pas se soucier des conditions de détention des criminels
les plus dangereux. Il n'a évoqué aucun pays ni aucun modèle pénitentiaire étranger.`

	mockContent := extractor.ExtractedContent{
		URL:     mockURL,
		Title:   "Positions politiques — entretiens croisés",
		Content: mockText,
	}

	ctx := context.Background()
	facts, err := extractorClient.ExtractFacts(ctx, "Marine Le Pen", mockContent)
	if err != nil {
		t.Fatalf("ExtractFacts failed: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected at least 1 fact, got 0")
	}
	for i, f := range facts {
		t.Logf("  [%d] entity=%q normalized=%q quote=%q", i+1, f.SubjectEntity, f.NormalizedFactText, f.OriginalQuote)
	}

	// Specifics that appear NOWHERE in the source — the extractor must not invent them.
	forbidden := []string{"GIEC", "IPCC", "Salvador", "Politico"}
	for _, bad := range forbidden {
		for i, f := range facts {
			hay := f.NormalizedFactText + " " + f.NormalizedFactTextEn + " " + f.OriginalQuote
			if strings.Contains(hay, bad) {
				t.Errorf("Fact %d over-specifies — injected %q absent from source: %q", i+1, bad, f.NormalizedFactText)
			}
		}
	}
}

// TestRemoteLLMExtractor_AttributionGrounding locks the attribution rules added
// after the 2026-07-21 prod audit (mirror-nik): (a) a social-media handle must
// not be resolved to a DIFFERENT politician (prod fact 10797 turned "@eciotti"
// into "Christian Estrosi"); (b) an aide's statement must not be attributed to
// the principal (prod fact 10919 turned "le bras droit de Mélenchon a dénoncé"
// into "Mélenchon a dénoncé"); (c) scope qualifiers must survive extraction
// (prod fact dropped "en partie" from "en partie responsable").
func TestRemoteLLMExtractor_AttributionGrounding(t *testing.T) {
	loadEnv(t)
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping attribution grounding test: OPEN_AI_KEY not set.")
	}

	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	mockText := `Le Pen à Nice, LFI en interne

Sur X, Marine Le Pen a écrit : « Avec mon ami @eciotti, aujourd'hui maire de cette
magnifique ville de Nice. Demain, j'assisterai à ses côtés aux cérémonies du 14 juillet. »

Au siège de La France insoumise, le bras droit de Jean-Luc Mélenchon a dénoncé
« l'entêtement de la candidature jusqu'au-boutiste » de Fabien Roussel.

Interrogé sur l'incendie de l'Aude, Jean-Luc Mélenchon a estimé que la politique
d'austérité était « en partie responsable » de la lenteur des secours.`

	mockContent := extractor.ExtractedContent{
		URL:     "http://example.com/attribution",
		Title:   "Le Pen à Nice, LFI en interne",
		Content: mockText,
	}

	ctx := context.Background()
	facts, err := extractorClient.ExtractFacts(ctx, "Marine Le Pen", mockContent)
	if err != nil {
		t.Fatalf("ExtractFacts failed: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected at least 1 fact, got 0")
	}
	for i, f := range facts {
		t.Logf("  [%d] entity=%q normalized=%q", i+1, f.SubjectEntity, f.NormalizedFactText)
	}

	for i, f := range facts {
		hay := f.NormalizedFactText + " " + f.NormalizedFactTextEn
		// (a) @eciotti must never become another politician (the prod bug named Estrosi).
		if strings.Contains(hay, "Estrosi") {
			t.Errorf("Fact %d resolved @eciotti to the wrong person (Estrosi): %q", i+1, f.NormalizedFactText)
		}
		// (b) the aide's denunciation must not be attributed to Mélenchon himself.
		if strings.Contains(strings.ToLower(f.SubjectEntity), "mélenchon") || strings.Contains(strings.ToLower(f.SubjectEntity), "melenchon") {
			if strings.Contains(strings.ToLower(hay), "roussel") {
				t.Errorf("Fact %d attributes the aide's denunciation of Roussel to Mélenchon: %q", i+1, f.NormalizedFactText)
			}
			// (c) if the austerity claim is extracted, the "en partie" qualifier must survive.
			if strings.Contains(strings.ToLower(hay), "responsable") && !strings.Contains(strings.ToLower(hay), "en partie") && !strings.Contains(strings.ToLower(hay), "partly") && !strings.Contains(strings.ToLower(hay), "in part") {
				t.Errorf("Fact %d dropped the 'en partie' qualifier: %q", i+1, f.NormalizedFactText)
			}
		}
	}
}

// TestRemoteLLMExtractor_ScopeRejection locks REJECT rule 8 (mirror-6ij): facts
// whose subject is a non-political actor (musicians, athletes) must not be
// extracted, while the political facts in the same article survive.
func TestRemoteLLMExtractor_ScopeRejection(t *testing.T) {
	loadEnv(t)
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping scope rejection test: OPEN_AI_KEY not set.")
	}

	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	mockText := `Une journée d'actualité chargée

Le ministre Gérald Darmanin a affirmé que la nouvelle loi pénale serait appliquée dès septembre, sans exception.

En marge de son concert à Paris, Jimin a déclaré que le public parisien était le plus grand de l'histoire de BTS et qu'il souhaitait revenir chaque année.

Didier Deschamps a annoncé la composition de l'équipe de France pour le match amical, avec trois nouveaux joueurs.`

	mockContent := extractor.ExtractedContent{
		URL:     "http://example.com/scope",
		Title:   "Une journée d'actualité chargée",
		Content: mockText,
	}

	ctx := context.Background()
	facts, err := extractorClient.ExtractFacts(ctx, "Gérald Darmanin", mockContent)
	if err != nil {
		t.Fatalf("ExtractFacts failed: %v", err)
	}
	var darmanin int
	for i, f := range facts {
		t.Logf("  [%d] entity=%q normalized=%q", i+1, f.SubjectEntity, f.NormalizedFactText)
		lower := strings.ToLower(f.SubjectEntity)
		if strings.Contains(lower, "jimin") || strings.Contains(lower, "deschamps") {
			t.Errorf("Fact %d extracted for out-of-scope subject %q: %q", i+1, f.SubjectEntity, f.NormalizedFactText)
		}
		if strings.Contains(lower, "darmanin") {
			darmanin++
		}
	}
	if darmanin == 0 {
		t.Error("expected at least one Darmanin fact to survive the scope rule")
	}
}
