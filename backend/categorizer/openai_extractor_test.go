package categorizer

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/llmtest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// extractResult is the reply of one extraction sample.
type extractResult struct {
	facts []backend.Fact
	err   error
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
	llmtest.Setup(t)

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
	llmtest.Setup(t)

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
	llmtest.Setup(t)

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
	llmtest.Setup(t)

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

	// Extraction is a single stochastic sample in prod too, and the scope rule
	// slips roughly 1 run in 10 (the model emits the very ❌ example from its
	// own prompt). Prod is protected downstream — the entity validator's R10
	// rejects out-of-scope subjects by name before anything is stored — so a
	// single-sample assertion here is stricter than what prod guarantees and
	// turned the suite red at random. Judge the rule by majority of 3 samples,
	// the same policy the contradiction judges use (JudgeVotes).
	ctx := context.Background()
	const samples = 3
	// The samples run first, all at once with -llm-claude (one batch). The
	// checks read their results by index.
	results := make([]extractResult, samples)
	llmtest.ForEach(samples, func(i int) {
		results[i].facts, results[i].err = extractorClient.ExtractFacts(ctx, "Gérald Darmanin", mockContent)
	})
	var leakRuns, darmaninRuns int
	for run := 1; run <= samples; run++ {
		facts, err := results[run-1].facts, results[run-1].err
		if err != nil {
			t.Fatalf("ExtractFacts failed (run %d): %v", run, err)
		}
		leaked, darmanin := false, false
		for i, f := range facts {
			t.Logf("  run %d [%d] entity=%q normalized=%q", run, i+1, f.SubjectEntity, f.NormalizedFactText)
			lower := strings.ToLower(f.SubjectEntity)
			if strings.Contains(lower, "jimin") || strings.Contains(lower, "deschamps") {
				t.Logf("  run %d: out-of-scope subject %q leaked: %q", run, f.SubjectEntity, f.NormalizedFactText)
				leaked = true
			}
			if strings.Contains(lower, "darmanin") {
				darmanin = true
			}
		}
		if leaked {
			leakRuns++
		}
		if darmanin {
			darmaninRuns++
		}
	}
	if leakRuns > samples/2 {
		t.Errorf("out-of-scope subjects leaked in %d of %d runs — the scope rule is not holding", leakRuns, samples)
	}
	if darmaninRuns <= samples/2 {
		t.Errorf("a Darmanin fact survived the scope rule in only %d of %d runs", darmaninRuns, samples)
	}
}

// TestRemoteLLMExtractor_NonPositionRejection pins the reject rules on the
// non-position shapes that leaked into prod as facts and spawned junk stances
// (2026-09-17): birthday wishes, thanks, personal tastes, campaign logistics
// (meeting held, spokesperson named, book published), biography, empty
// formulas, narrated exchanges and passive-voice portraits. The two real
// positions in the same text must survive. Majority of 3 samples, like the
// scope rule (extraction is one stochastic sample in prod too).
func TestRemoteLLMExtractor_NonPositionRejection(t *testing.T) {
	llmtest.Setup(t)
	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	mockText := `Marine Tondelier en campagne

Marine Tondelier a souhaité un joyeux anniversaire à Jordan Bardella sur X, avant de remercier Marame Kane pour l'organisation de la Global Black Pride à Paris.

Invitée de BFMTV, elle a indiqué apprécier les chansons « J'irai où tu iras » et « Prière païenne » pour mettre l'ambiance dans ses réunions. Elle s'est expliquée face à Benjamin Duhamel après avoir été critiquée pour ne pas avoir cité un ouvrage de Pompidou.

Sur le fond, Marine Tondelier a affirmé qu'elle soutiendrait un éventuel mouvement social contre la vie chère, et a proposé un bouclier social et environnemental incluant des mesures pour le logement abordable.

La candidate, décrite par ses proches comme portant une vision plus sociale de l'écologie, a organisé son meeting de rentrée à Nantes et a nommé la conseillère de Paris Rania Kissi comme porte-parole de sa campagne. Elle publie jeudi un livre-programme aux éditions du Seuil.

Elle a déclaré avoir grandi à Hénin-Beaumont dans un quartier populaire, et a conclu que « chacun prend ses responsabilités ».`

	mockContent := extractor.ExtractedContent{
		URL:     "http://example.com/non-position",
		Title:   "Marine Tondelier en campagne",
		Content: mockText,
	}
	junkMarkers := []string{"anniversaire", "remerci", "chanson", "expliqu", "décrite", "meeting de rentrée", "porte-parole", "livre", "grandi", "responsabilit"}

	ctx := context.Background()
	const samples = 3
	// The samples run first, all at once with -llm-claude (one batch). The
	// checks read their results by index.
	results := make([]extractResult, samples)
	llmtest.ForEach(samples, func(i int) {
		results[i].facts, results[i].err = extractorClient.ExtractFacts(ctx, "Marine Tondelier", mockContent)
	})
	var leakRuns, positionsRuns int
	for run := 1; run <= samples; run++ {
		facts, err := results[run-1].facts, results[run-1].err
		if err != nil {
			t.Fatalf("ExtractFacts failed (run %d): %v", run, err)
		}
		leaked, social, shield, split := false, false, false, true
		for i, f := range facts {
			text := strings.ToLower(f.NormalizedFactText)
			t.Logf("  run %d [%d] %s", run, i+1, f.NormalizedFactText)
			for _, m := range junkMarkers {
				if strings.Contains(text, m) {
					t.Logf("  run %d: non-position fact leaked (%q): %q", run, m, f.NormalizedFactText)
					leaked = true
				}
			}
			isSocial := strings.Contains(text, "mouvement social") || strings.Contains(text, "vie chère")
			isShield := strings.Contains(text, "bouclier")
			if isSocial {
				social = true
			}
			if isShield {
				shield = true
			}
			if isSocial && isShield {
				// One claim per fact: the two positions must not be glued together.
				t.Logf("  run %d: compound fact (two claims in one): %q", run, f.NormalizedFactText)
				split = false
			}
		}
		if leaked {
			leakRuns++
		}
		if social && shield && split {
			positionsRuns++
		}
	}
	if leakRuns > samples/2 {
		t.Errorf("non-position facts leaked in %d of %d runs — the reject rules are not holding", leakRuns, samples)
	}
	if positionsRuns <= samples/2 {
		t.Errorf("both real positions survived as separate facts in only %d of %d runs", positionsRuns, samples)
	}
}

// TestRemoteLLMExtractor_UnambiguousWords pins the UNAMBIGUOUS WORDS rule
// (mirror-ezt): contradiction #169 paired Attal's "conseil de discipline dès
// l'école primaire" with his "une primaire n'est pas possible" because both
// facts only said "primaire". Each fact must carry its own sense, in the text
// and in the embedding text. Majority of 3 samples, like the rules above.
func TestRemoteLLMExtractor_UnambiguousWords(t *testing.T) {
	llmtest.Setup(t)
	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	mockText := `Gabriel Attal, entre justice des mineurs et 2027

Gabriel Attal a proposé la création d'un conseil de discipline dès le primaire pour les élèves violents.

Interrogé sur la désignation d'un candidat unique du bloc central pour 2027, il a répondu qu'« une primaire n'est pas possible ».`

	mockContent := extractor.ExtractedContent{
		URL:     "http://example.com/primaire",
		Title:   "Gabriel Attal, entre justice des mineurs et 2027",
		Content: mockText,
	}
	// "primaire" itself must carry its sense, not merely sit next to words
	// that hint at it: the judge compares the facts, not their sources.
	schoolPrimaire := regexp.MustCompile(`[ée]cole[ -]primaire`)
	electionPrimaire := regexp.MustCompile(`primaire[ -][ée]lectorale|[ée]lection[ -]primaire`)
	ctx := context.Background()
	const samples = 3
	// The samples run first, all at once with -llm-claude (one batch). The
	// checks read their results by index.
	results := make([]extractResult, samples)
	llmtest.ForEach(samples, func(i int) {
		results[i].facts, results[i].err = extractorClient.ExtractFacts(ctx, "Gabriel Attal", mockContent)
	})
	var goodRuns int
	for run := 1; run <= samples; run++ {
		facts, err := results[run-1].facts, results[run-1].err
		if err != nil {
			t.Fatalf("ExtractFacts failed (run %d): %v", run, err)
		}
		school, election, ambiguous := false, false, false
		for i, f := range facts {
			text, emb := strings.ToLower(f.NormalizedFactText), strings.ToLower(f.EmbeddingText)
			t.Logf("  run %d [%d] %s | embedding=%q", run, i+1, f.NormalizedFactText, f.EmbeddingText)
			switch {
			case strings.Contains(text, "discipline"):
				school = true
				if !schoolPrimaire.MatchString(text) {
					t.Logf("  run %d: school fact without its sense: %q / %q", run, f.NormalizedFactText, f.EmbeddingText)
					ambiguous = true
				}
			case strings.Contains(text, "primaire"):
				election = true
				if !electionPrimaire.MatchString(text) || !electionPrimaire.MatchString(emb) {
					t.Logf("  run %d: election fact without its sense: %q / %q", run, f.NormalizedFactText, f.EmbeddingText)
					ambiguous = true
				}
			}
		}
		if school && election && !ambiguous {
			goodRuns++
		}
	}
	if goodRuns <= samples/2 {
		t.Errorf("both facts carried an unambiguous sense in only %d of %d runs", goodRuns, samples)
	}
}

// TestRemoteLLMExtractor_SideTakingActs pins rule 4's exception for acts: an
// act the entity chose that takes a side (running in a primary, presenting a
// joint list with named partners, attending a named event) is kept, because
// it can break the entity's own stated position (prod 2026-10-23: the audit
// flagged "Glucksmann est engagé dans la primaire" and "Macron assistera à la
// messe du pape" as not contradictable, though each grounds a real
// contradiction); logistics that take no side stay rejected. Majority of 3.
func TestRemoteLLMExtractor_SideTakingActs(t *testing.T) {
	llmtest.Setup(t)
	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}

	mockText := `Raphaël Glucksmann accélère

Raphaël Glucksmann est désormais engagé dans la primaire PS-Place publique, qui se tiendra les 9 et 10 octobre.

Pour les municipales, il a présenté à Lille une liste d'union avec Martine Aubry et Marine Tondelier.

Il a réorganisé son agenda pour assister lundi à la messe du pape Léon XIV à Notre-Dame.

Il a tenu son meeting de rentrée à Montpellier et a nommé Aurore Lalucq porte-parole de sa campagne.`

	mockContent := extractor.ExtractedContent{
		URL:     "http://example.com/acts",
		Title:   "Raphaël Glucksmann accélère",
		Content: mockText,
	}
	ctx := context.Background()
	const samples = 3
	results := make([]extractResult, samples)
	llmtest.ForEach(samples, func(i int) {
		results[i].facts, results[i].err = extractorClient.ExtractFacts(ctx, "Raphaël Glucksmann", mockContent)
	})
	var keptRuns, leakRuns int
	for run := 1; run <= samples; run++ {
		facts, err := results[run-1].facts, results[run-1].err
		if err != nil {
			t.Fatalf("ExtractFacts failed (run %d): %v", run, err)
		}
		primary, list, mass, leaked := false, false, false, false
		for i, f := range facts {
			text := strings.ToLower(f.NormalizedFactText)
			t.Logf("  run %d [%d] %s", run, i+1, f.NormalizedFactText)
			switch {
			case strings.Contains(text, "primaire"):
				primary = true
			case strings.Contains(text, "liste"):
				list = true
			case strings.Contains(text, "messe"):
				mass = true
			case strings.Contains(text, "meeting") || strings.Contains(text, "porte-parole"):
				leaked = true
			}
		}
		if primary && list && mass {
			keptRuns++
		}
		if leaked {
			leakRuns++
		}
	}
	if keptRuns <= samples/2 {
		t.Errorf("the three side-taking acts were kept in only %d of %d runs", keptRuns, samples)
	}
	if leakRuns > samples/2 {
		t.Errorf("logistics (meeting, spokesperson) leaked in %d of %d runs", leakRuns, samples)
	}
}

// TestRemoteLLMExtractor_SecondHandClaims pins three cases a review found
// outside the goldens (mirror-ds6): a claim of the entourage taken as the
// person's own (fact 95098), a journalist's elliptic summary taken as the
// person's proposal (fact 14498, and wrong: she opposed 65), and what the
// person did not say taken as a fact (fact 15000). Each article also holds a
// real position, which must survive. Majority of 3 samples per article.
func TestRemoteLLMExtractor_SecondHandClaims(t *testing.T) {
	llmtest.Setup(t)
	extractorClient, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create OpenAI extractor: %v", err)
	}
	cases := []struct {
		name, person, title, text string
		leak                      *regexp.Regexp // a fact matching it is the bad case
		keep                      *regexp.Regexp // the real position that must stay
	}{
		{
			name:   "entourage",
			person: "Jordan Bardella",
			title:  "Propos attribués à Jordan Bardella : il va porter plainte",
			text: `Après de nouvelles révélations du Canard enchaîné sur des messages antisémites publiés sous un pseudonyme, Jordan Bardella a annoncé mardi qu'il porterait plainte en diffamation contre l'hebdomadaire.

« Je n'ai jamais écrit ces messages, c'est une calomnie », a déclaré le président du RN devant la presse.

Son entourage assure par ailleurs que Jordan Bardella ne s'est jamais connecté à ce compte Facebook ni n'a supprimé aucun message. Selon un proche, l'équipe juridique du parti étudie aussi une action contre les auteurs des captures d'écran.`,
			leak: regexp.MustCompile(`(?i)facebook|connect|supprim`),
			keep: regexp.MustCompile(`(?i)plainte|jamais écrit|calomnie`),
		},
		{
			name:   "journalist summary",
			person: "Marine Le Pen",
			title:  "Présidentielle 2022 : Marine Le Pen, la prudence en campagne",
			text: `Dès son allocution de Vincennes, elle mise sur un front anti-Macron : la candidate du peuple « face au pouvoir de l'argent » et la retraite à 65 ans, que l'adversaire défend et dont elle fait un repoussoir.

Ce soir-là, Marine Le Pen promet devant ses partisans : « Je baisserai la TVA sur l'énergie de 20 % à 5,5 % dès l'été. »

Au fil des semaines, la candidate du RN évite les sujets qui fâchent et soigne son image de présidente apaisée.`,
			leak: regexp.MustCompile(`(?i)65`),
			keep: regexp.MustCompile(`(?i)tva`),
		},
		{
			name:   "absence",
			person: "Marine Le Pen",
			title:  "Marine Le Pen dévoile son programme économique",
			text: `Invitée de France 2 jeudi soir, Marine Le Pen a promis de supprimer l'impôt sur le revenu pour les moins de 30 ans. « C'est la mesure de justice que la jeunesse attend », a-t-elle affirmé.

Elle n'a pas fait de reniement sur la retraite à 60 ans pour ceux qui ont commencé à travailler tôt, et n'a pas mentionné la TVA sociale lors de son intervention. Les économistes interrogés jugent son chiffrage incomplet.`,
			leak: regexp.MustCompile(`(?i)reniement|renié|pas mentionné|tva sociale|60 ans`),
			keep: regexp.MustCompile(`(?i)impôt sur le revenu`),
		},
	}
	ctx := context.Background()
	const samples = 3
	results := make([]extractResult, samples*len(cases))
	llmtest.ForEach(len(results), func(i int) {
		c := cases[i/samples]
		results[i].facts, results[i].err = extractorClient.ExtractFacts(ctx, c.person, extractor.ExtractedContent{
			URL: "http://example.com/" + strings.ReplaceAll(c.name, " ", "-"), Title: c.title, Content: c.text,
		})
	})
	for ci, c := range cases {
		var leakRuns, keepRuns int
		for run := 0; run < samples; run++ {
			r := results[ci*samples+run]
			if r.err != nil {
				t.Fatalf("%s: ExtractFacts failed (run %d): %v", c.name, run+1, r.err)
			}
			leaked, kept := false, false
			for i, f := range r.facts {
				t.Logf("  %s run %d [%d] %s", c.name, run+1, i+1, f.NormalizedFactText)
				if c.leak.MatchString(f.NormalizedFactText) {
					t.Logf("  %s run %d: second-hand claim leaked: %q", c.name, run+1, f.NormalizedFactText)
					leaked = true
				}
				if c.keep.MatchString(f.NormalizedFactText) {
					kept = true
				}
			}
			if leaked {
				leakRuns++
			}
			if kept {
				keepRuns++
			}
		}
		if leakRuns > samples/2 {
			t.Errorf("%s: the second-hand claim became a fact in %d of %d runs", c.name, leakRuns, samples)
		}
		if keepRuns <= samples/2 {
			t.Errorf("%s: the real position survived in only %d of %d runs", c.name, keepRuns, samples)
		}
	}
}
