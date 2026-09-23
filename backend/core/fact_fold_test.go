package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// foldFakeStorer extends fakeInserterStorer with a same-claim match and records
// every group merge, so the fold path can be asserted without a DB.
type foldFakeStorer struct {
	fakeInserterStorer
	similar []SimilarFact

	newIsMoreConcrete bool       // MostConcreteFact picks the new fact over the matched one
	merges            [][2]int64 // (from, into)
}

func (f *foldFakeStorer) FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, factText string, textFloor float64, limit int) ([]SimilarFact, error) {
	return f.similar, nil
}

// matchedFact is the similar fact the fixtures return: same canonical verb as
// the input so the tier-1 auto-dedupe rule fires, carried by a single person
// (the fold path refuses shared facts) unless mentionPersons says otherwise.
func matchedFact(text string, mentionPersons int) SimilarFact {
	return SimilarFact{ID: 7, Text: text, Distance: 0.05, CanonicalVerb: "affirmer", MentionPersons: mentionPersons}
}

// MostConcreteFact is called with (matched, new).
func (f *foldFakeStorer) MostConcreteFact(ctx context.Context, factIDs []int64) (int64, error) {
	if f.newIsMoreConcrete {
		return factIDs[1], nil
	}
	return factIDs[0], nil
}

func (f *foldFakeStorer) MergeFactIntoGroup(ctx context.Context, from, into int64) error {
	f.merges = append(f.merges, [2]int64{from, into})
	return nil
}

func (f *foldFakeStorer) RefreshPersonScore(ctx context.Context, personID int64) error {
	return nil
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
// with folding enabled must KEEP the fact (inserted, not dropped), put it in
// the matched fact's group, and skip stance analysis.
func TestFoldGroupsNewFactUnderMatchedFact(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.", 1)}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableFolding(st)

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 || res.Skipped != 0 {
		t.Fatalf("fact must be inserted (kept), got inserted=%d skipped=%d", res.Inserted, res.Skipped)
	}
	if len(st.merges) != 1 || st.merges[0] != [2]int64{1, 7} {
		t.Fatalf("the new fact (1) must join the matched fact's group (7), got %v", st.merges)
	}
	if stance.calls != 0 {
		t.Fatalf("stance analysis must be skipped for grouped facts, ran %d times", stance.calls)
	}
}

// When the new wording is more concrete, it becomes the group's parent and
// the matched fact moves under it.
func TestFoldPromotesMoreConcreteNewFact(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.", 1)}, newIsMoreConcrete: true}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableFolding(st)

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted, got %+v", res)
	}
	if len(st.merges) != 1 || st.merges[0] != [2]int64{7, 1} {
		t.Fatalf("the matched fact (7) must move under the new parent (1), got %v", st.merges)
	}
	if stance.calls != 0 {
		t.Fatalf("stance analysis must be skipped for grouped facts, ran %d times", stance.calls)
	}
}

// The LLM judge is the LAST decider: even a very-high-similarity candidate is
// NOT folded when the judge says the positions differ — the fact goes through
// normal stance analysis instead.
func TestFoldVetoedByJudge(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.", 1)}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: false}
	fi.EnableFolding(st)

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if len(st.merges) != 0 {
		t.Fatalf("judge veto must prevent any grouping, got %v", st.merges)
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
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("a voté POUR le texte.", 3)}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableFolding(st)

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if len(st.merges) != 0 {
		t.Fatalf("shared matched fact must not be grouped, got %v", st.merges)
	}
	if stance.calls != 1 {
		t.Fatalf("unfolded fact must go through stance analysis, ran %d times", stance.calls)
	}
}

// A NEW fact that itself has co-actors (shared vote fact) must never fold under
// an existing meta either — same global-duplicate hazard, from the other side.
func TestFoldRefusedOnNewSharedFact(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.", 1)}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.EnableFolding(st)

	input := foldInput()
	input.CoActorIDs = []int64{201, 202}
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{input})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("fact must be inserted normally, got %+v", res)
	}
	if len(st.merges) != 0 {
		t.Fatalf("co-actor fact must not be grouped, got %v", st.merges)
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
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.", 1)}}
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

// flakyJudge fails its first `failures` calls, then answers same.
type flakyJudge struct {
	failures int
	same     bool
	calls    int
}

func (j *flakyJudge) IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error) {
	j.calls++
	if j.calls <= j.failures {
		return false, errors.New("llm unavailable")
	}
	return j.same, nil
}

// A gate call that fails once is retried and its verdict applied: the fact is
// deduplicated instead of the failure counting as "not a duplicate" forever.
func TestDedupeGateRetriesOnceOnError(t *testing.T) {
	// Two different direct-claim verbs keep the cheap tier out of the way so
	// the gate is consulted.
	candidate := matchedFact("Il clame son innocence.", 1)
	candidate.CanonicalVerb = "CLAIMED"
	st := &foldFakeStorer{similar: []SimilarFact{candidate}}
	fi := NewFactInserter(st, &countingStance{})
	judge := &flakyJudge{failures: 1, same: true}
	fi.dedupeJudge = judge
	input := foldInput()
	input.CanonicalVerb = "PROMISED"

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{input})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if judge.calls != 2 {
		t.Fatalf("the gate must be asked twice (one failure, one retry), got %d calls", judge.calls)
	}
	if res.Inserted != 0 || res.Skipped != 1 {
		t.Fatalf("the retried verdict must apply (fact dropped), got %+v", res)
	}
}

// Two consecutive failures give up on that candidate and the fact is inserted.
func TestDedupeGateGivesUpAfterTwoFailures(t *testing.T) {
	candidate := matchedFact("Il clame son innocence.", 1)
	candidate.CanonicalVerb = "CLAIMED"
	st := &foldFakeStorer{similar: []SimilarFact{candidate}}
	fi := NewFactInserter(st, &countingStance{})
	judge := &flakyJudge{failures: 2, same: true}
	fi.dedupeJudge = judge
	input := foldInput()
	input.CanonicalVerb = "PROMISED"

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{input})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if judge.calls != 2 || res.Inserted != 1 {
		t.Fatalf("after two failures the fact is inserted, got calls=%d res=%+v", judge.calls, res)
	}
}
