package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// foldFakeStorer extends fakeInserterStorer with a same-claim match and records
// every MetaFoldStorer call, so the fold path can be asserted without a DB.
type foldFakeStorer struct {
	fakeInserterStorer
	similar []SimilarFact

	existingMeta   int64 // 0 = matched fact has no meta yet
	mentionPersons int   // 0 = default (1 person); >1 marks the matched fact shared
	createdMeta    int64
	updatedText    string
	markedMembers  []int64
	repointedSpecs []int64
	stanceAttached []int64
}

func (f *foldFakeStorer) FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, limit int) ([]SimilarFact, error) {
	return f.similar, nil
}

// Same canonical verb as the input so the tier-1 auto-dedupe rule fires.
func (f *foldFakeStorer) GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error) {
	return "affirmer", nil
}

func (f *foldFakeStorer) GetMetaForFact(ctx context.Context, factID int64) (int64, bool, error) {
	if f.existingMeta != 0 {
		return f.existingMeta, true, nil
	}
	return 0, false, nil
}

func (f *foldFakeStorer) CreateMetaFact(ctx context.Context, personID int64, text, embeddingText string, memberEmbedding []float32, lang, canonicalVerb string) (int64, error) {
	f.createdMeta = 900
	f.updatedText = text
	return f.createdMeta, nil
}

func (f *foldFakeStorer) UpdateMetaFactText(ctx context.Context, metaID int64, text string) error {
	f.updatedText = text
	return nil
}

func (f *foldFakeStorer) GetMetaMemberTexts(ctx context.Context, metaID int64) ([]string, error) {
	return []string{"member one text"}, nil
}

func (f *foldFakeStorer) MarkFactAsDuplicate(ctx context.Context, duplicateID, canonicalID int64) error {
	f.markedMembers = append(f.markedMembers, duplicateID)
	return nil
}

func (f *foldFakeStorer) RepointFactDuplicate(ctx context.Context, general, specific int64) (int, int, error) {
	f.repointedSpecs = append(f.repointedSpecs, specific)
	return 0, 0, nil
}

func (f *foldFakeStorer) GetFactStanceID(ctx context.Context, personID int64, factID int64) (int64, bool, error) {
	return 42, true, nil
}

// Single-mention facts by default: the fold path refuses shared (co-actor)
// facts, and these tests exercise the non-shared case unless overridden.
func (f *foldFakeStorer) CountFactMentionPersons(ctx context.Context, factID int64) (int, error) {
	if f.mentionPersons != 0 {
		return f.mentionPersons, nil
	}
	return 1, nil
}

func (f *foldFakeStorer) AddFactToStance(ctx context.Context, stanceID, factID int64) error {
	f.stanceAttached = append(f.stanceAttached, factID)
	return nil
}

func (f *foldFakeStorer) RefreshPersonScore(ctx context.Context, personID int64) error {
	return nil
}

type fakeSynth struct{}

func (fakeSynth) SynthesizeMetaFact(ctx context.Context, subject string, memberTexts []string, lang string) (string, error) {
	return "synthesized: " + strings.Join(memberTexts, " | "), nil
}

// fakeJudge stands in for the LLM gate — the mandatory last decider on fold
// membership (embeddings only select candidates).
type fakeJudge struct{ same bool }

func (j fakeJudge) IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error) {
	return j.same, nil
}

func (j fakeJudge) IsSamePosition(ctx context.Context, subject, factA, factB string) (bool, error) {
	return j.same, nil
}

// countingStance fails the test if stance analysis runs for a folded fact.
type countingStance struct{ calls int }

func (c *countingStance) ProcessNewFact(ctx context.Context, personID, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	c.calls++
	return &StanceProcessResult{Verdict: "NEW_STANCE", StanceID: 1}, nil
}

func foldInput() StoreFactInput {
	now := time.Now()
	return StoreFactInput{
		SubjectEntity:      "test person",
		NormalizedFactText: "Il affirme son innocence dans l'affaire.",
		EmbeddingText:      "innocence affaire",
		Embedding:          []float32{1, 0, 0},
		OriginalQuote:      "je suis innocent",
		CanonicalVerb:      "affirmer",
		SourceURL:          "http://ex/1",
		SourceType:         "news",
		PublishedAt:        &now,
		Lang:               "fr",
	}
}

// A same-claim hit (confirmed by the LLM judge — the mandatory last decider)
// with folding enabled must KEEP the fact (inserted, not dropped), link it
// under a freshly created meta, and skip stance analysis.
func TestFoldNewFactCreatesMeta(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{{ID: 7, Text: "Il clame son innocence.", Distance: 0.05}}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableMetaFolding(st, fakeSynth{})

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 || res.Skipped != 0 {
		t.Fatalf("fact must be inserted (kept), got inserted=%d skipped=%d", res.Inserted, res.Skipped)
	}
	if st.createdMeta == 0 {
		t.Fatal("expected a meta-fact to be created")
	}
	// Both the matched fact (7) and the new fact (1) become members.
	if len(st.markedMembers) != 2 || st.markedMembers[0] != 7 || st.markedMembers[1] != 1 {
		t.Fatalf("expected members [7 1], got %v", st.markedMembers)
	}
	if len(st.repointedSpecs) != 2 {
		t.Fatalf("expected both members repointed, got %v", st.repointedSpecs)
	}
	if stance.calls != 0 {
		t.Fatalf("stance analysis must be skipped for folded facts, ran %d times", stance.calls)
	}
	if !strings.Contains(st.updatedText, "innocence") {
		t.Fatalf("meta text not synthesized from members: %q", st.updatedText)
	}
}

// When the matched fact already belongs to a meta, the new fact joins it and the
// meta text is re-synthesized over all members including the new one.
func TestFoldNewFactJoinsExistingMeta(t *testing.T) {
	st := &foldFakeStorer{
		similar:      []SimilarFact{{ID: 7, Text: "Il clame son innocence.", Distance: 0.05}},
		existingMeta: 500,
	}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableMetaFolding(st, fakeSynth{})

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted, got %+v", res)
	}
	if st.createdMeta != 0 {
		t.Fatal("must reuse the existing meta, not create a new one")
	}
	if len(st.markedMembers) != 1 || st.markedMembers[0] != 1 {
		t.Fatalf("only the new fact should be marked, got %v", st.markedMembers)
	}
	if !strings.Contains(st.updatedText, "member one text") || !strings.Contains(st.updatedText, "innocence dans l'affaire") {
		t.Fatalf("meta must be re-synthesized over old + new members: %q", st.updatedText)
	}
}

// The LLM judge is the LAST decider: even a very-high-similarity candidate is
// NOT folded when the judge says the positions differ — the fact goes through
// normal stance analysis instead.
func TestFoldVetoedByJudge(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{{ID: 7, Text: "Il clame son innocence.", Distance: 0.05}}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: false}
	fi.EnableMetaFolding(st, fakeSynth{})

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if st.createdMeta != 0 || len(st.markedMembers) != 0 {
		t.Fatalf("judge veto must prevent any fold, got meta=%d members=%v", st.createdMeta, st.markedMembers)
	}
	if stance.calls != 1 {
		t.Fatalf("vetoed fact must go through stance analysis, ran %d times", stance.calls)
	}
}

// A matched fact carried by several persons (shared co-actor fact, e.g. a vote)
// must NEVER become a fold member: 'duplicate' is a global flag, so folding it
// would remove the fact from every other voter's active set. The new fact goes
// through normal stance analysis instead.
func TestFoldRefusedOnSharedMatchedFact(t *testing.T) {
	st := &foldFakeStorer{
		similar:        []SimilarFact{{ID: 7, Text: "a voté POUR le texte.", Distance: 0.05}},
		mentionPersons: 3,
	}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableMetaFolding(st, fakeSynth{})

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if st.createdMeta != 0 || len(st.markedMembers) != 0 {
		t.Fatalf("shared matched fact must not fold, got meta=%d members=%v", st.createdMeta, st.markedMembers)
	}
	if stance.calls != 1 {
		t.Fatalf("unfolded fact must go through stance analysis, ran %d times", stance.calls)
	}
}

// A NEW fact that itself has co-actors (shared vote fact) must never fold under
// an existing meta either — same global-duplicate hazard, from the other side.
func TestFoldRefusedOnNewSharedFact(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{{ID: 7, Text: "Il clame son innocence.", Distance: 0.05}}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableMetaFolding(st, fakeSynth{})

	input := foldInput()
	input.CoActorIDs = []int64{201, 202}
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{input})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if st.createdMeta != 0 || len(st.markedMembers) != 0 {
		t.Fatalf("co-actor fact must not fold, got meta=%d members=%v", st.createdMeta, st.markedMembers)
	}
	if stance.calls != 1 {
		t.Fatalf("co-actor fact must go through stance analysis, ran %d times", stance.calls)
	}
}

// Without folding enabled, the same-claim fact is not inserted — but its
// source (and mentions) are attached to the existing fact as additional
// provenance: the claim is known, where it was said again is new, and
// provenance-based producers (same séance as a vote) depend on it.
func TestSameClaimDroppedWhenFoldingDisabled(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{{ID: 7, Text: "Il clame son innocence.", Distance: 0.05}}}
	fi := NewFactInserter(st, &countingStance{})

	input := foldInput()
	input.CoActorIDs = []int64{42}
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{input})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 0 || res.Skipped != 1 {
		t.Fatalf("fact must be dropped without folding, got %+v", res)
	}
	if len(st.linkedSources) != 1 || st.linkedSources[0][0] != int64(7) || st.linkedSources[0][1] != "http://ex/1" {
		t.Fatalf("the skipped fact's source must be attached to the existing fact 7, got %v", st.linkedSources)
	}
	if got := st.mentioned[st.nextSrcID]; len(got) != 2 || got[0] != 1 || got[1] != 42 {
		t.Fatalf("subject and co-actors must be mentioned on the attached source, got %v", got)
	}
}
