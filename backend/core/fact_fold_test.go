package core

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// foldFakeStorer extends fakeInserterStorer with a same-claim match and records
// every group merge, so the fold path can be asserted without a DB.
type foldFakeStorer struct {
	fakeInserterStorer
	similar []SimilarFact
	queries []SimilarFactQuery // every candidate lookup

	newIsMoreConcrete bool       // MostConcreteFact picks the new fact over the matched one
	merges            [][2]int64 // (from, into)
	sources           map[int64][]ClaimSource
}

func (f *foldFakeStorer) LoadClaimSources(ctx context.Context, factIDs []int64, sourceTypes []string) (map[int64][]ClaimSource, error) {
	return f.sources, nil
}

func (f *foldFakeStorer) ListGroupEvidencePairs(ctx context.Context, pairs [][2]int64, rule GroupEvidence) ([][2]int64, error) {
	return pairs, nil
}

func (f *foldFakeStorer) FindSimilarActiveFacts(ctx context.Context, q SimilarFactQuery) ([]SimilarFact, error) {
	f.queries = append(f.queries, q)
	return f.similar, nil
}

// testFreeTextSource is the free-text source type of the core tests: a fact
// of that type can be grouped.
const testFreeTextSource = "test_free_text"

func init() { RequireQuoteForSourceType(testFreeTextSource) }

// matchedFact is the similar fact the fixtures return: very close to the
// input and with the same canonical verb, so only the judge can refuse it.
func matchedFact(text string) SimilarFact {
	return SimilarFact{ID: 7, Text: text, Distance: 0.05, CanonicalVerb: "affirmer"}
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

func (j fakeJudge) IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error) {
	return verdictOf(j.same), nil
}

func verdictOf(same bool) OccurrenceVerdict {
	if same {
		return OccurrenceSame
	}
	return OccurrenceDifferent
}

// countingStance fails the test if stance analysis runs for a folded fact.
type countingStance struct{ calls int }

func (c *countingStance) ProcessNewFact(ctx context.Context, personID, factID int64, factText string, embedding []float32, verb string, _ *time.Time) (*StanceProcessResult, error) {
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
		SourceType:         testFreeTextSource,
		PublishedAt:        &now,
		Lang:               "fr",
	}
}

// A same-claim hit (confirmed by the LLM judge — the mandatory last decider)
// must KEEP the fact (inserted, not dropped), put it in
// the matched fact's group, and skip stance analysis.
func TestFoldGroupsNewFactUnderMatchedFact(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.")}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.groupStorer = st

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
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.")}, newIsMoreConcrete: true}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: true}
	fi.groupStorer = st

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
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.")}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	fi.dedupeJudge = fakeJudge{same: false}
	fi.groupStorer = st

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

// flakyJudge fails its first `failures` calls, then answers same.
type flakyJudge struct {
	failures  int
	same      bool
	contained bool // answer "one fact holds the other's claim plus more"
	calls     int
	asked     [][2]ClaimEvidence // (existing, incoming) of every call
}

func (j *flakyJudge) IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error) {
	j.calls++
	j.asked = append(j.asked, [2]ClaimEvidence{a, b})
	if j.calls <= j.failures {
		return OccurrenceDifferent, errors.New("llm unavailable")
	}
	if j.contained {
		return OccurrenceContained, nil
	}
	return verdictOf(j.same), nil
}

// A gate call that fails once is retried and its verdict applied: the fact is
// grouped instead of the failure counting as "not the same claim" forever.
func TestDedupeGateRetriesOnceOnError(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.")}}
	fi := NewFactInserter(st, &countingStance{})
	judge := &flakyJudge{failures: 1, same: true}
	fi.dedupeJudge = judge
	fi.groupStorer = st

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if judge.calls != 2 {
		t.Fatalf("the gate must be asked twice (one failure, one retry), got %d calls", judge.calls)
	}
	if res.Inserted != 1 || len(st.merges) != 1 {
		t.Fatalf("the retried verdict must apply (fact grouped), got res=%+v merges=%v", res, st.merges)
	}
}

// Two consecutive failures give up on that candidate and the fact is inserted standalone.
func TestDedupeGateGivesUpAfterTwoFailures(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il clame son innocence.")}}
	fi := NewFactInserter(st, &countingStance{})
	judge := &flakyJudge{failures: 2, same: true}
	fi.dedupeJudge = judge
	fi.groupStorer = st

	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if judge.calls != 2 || res.Inserted != 1 || len(st.merges) != 0 {
		t.Fatalf("after two failures the fact is inserted standalone, got calls=%d res=%+v merges=%v", judge.calls, res, st.merges)
	}
}

// The candidate lookup carries the evidence rule with the new fact's quote
// and date, and the gate reads both facts with their sources.
func TestDedupeGateReadsEvidence(t *testing.T) {
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	st := &foldFakeStorer{
		similar: []SimilarFact{matchedFact("Il clame son innocence.")},
		sources: map[int64][]ClaimSource{7: {{Outlet: "lemonde.fr", Quote: "je suis innocent, dit-il", PublishedAt: &old}}},
	}
	fi := NewFactInserter(st, &countingStance{})
	judge := &flakyJudge{same: true}
	fi.dedupeJudge = judge
	fi.groupStorer = st

	in := foldInput()
	in.SourceURL = "https://www.liberation.fr/politique/a"
	if _, err := fi.InsertFacts(context.Background(), []StoreFactInput{in}); err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if len(st.queries) != 1 {
		t.Fatalf("want one candidate lookup, got %d", len(st.queries))
	}
	q := st.queries[0]
	if q.Evidence == nil || q.Evidence.QuoteFloor != groupQuoteSimilarityFloor || q.Evidence.MaxDateGapDays != groupMaxDateGapDays ||
		!slices.Contains(q.Evidence.SourceTypes, testFreeTextSource) || q.Quote != "je suis innocent" || q.PublishedAt != in.PublishedAt {
		t.Fatalf("candidate lookup without the evidence rule: %+v", q)
	}
	if len(judge.asked) != 1 {
		t.Fatalf("want one gate call, got %d", len(judge.asked))
	}
	existing, incoming := judge.asked[0][0], judge.asked[0][1]
	if existing.Text != "Il clame son innocence." || len(existing.Sources) != 1 || existing.Sources[0].Outlet != "lemonde.fr" {
		t.Fatalf("the gate must read the matched fact with its sources, got %+v", existing)
	}
	if len(incoming.Sources) != 1 || incoming.Sources[0].Outlet != "liberation.fr" || incoming.Sources[0].Quote != "je suis innocent" || incoming.Sources[0].PublishedAt != in.PublishedAt {
		t.Fatalf("the gate must read the new fact with its outlet, quote and date, got %+v", incoming)
	}
}

// A contained pair is grouped with the fuller fact as the parent, whatever
// MostConcreteFact would pick.
func TestFoldContainedPairKeepsFullerFactAsParent(t *testing.T) {
	short := matchedFact("Il clame son innocence.")
	st := &foldFakeStorer{similar: []SimilarFact{short}} // MostConcreteFact would pick the matched fact (7)
	fi := NewFactInserter(st, &countingStance{})
	fi.dedupeJudge = &flakyJudge{contained: true}
	fi.groupStorer = st

	if _, err := fi.InsertFacts(context.Background(), []StoreFactInput{foldInput()}); err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if len(st.merges) != 1 || st.merges[0] != [2]int64{7, 1} {
		t.Fatalf("the shorter matched fact (7) must move under the fuller new fact (1), got %v", st.merges)
	}
}

// A vote (a structured record, not free text) is never grouped: no candidate
// lookup, no gate call, normal stance routing.
func TestStructuredFactIsNeverGrouped(t *testing.T) {
	st := &foldFakeStorer{similar: []SimilarFact{matchedFact("Il a voté POUR le texte.")}}
	stance := &countingStance{}
	fi := NewFactInserter(st, stance)
	judge := &flakyJudge{same: true}
	fi.dedupeJudge = judge
	fi.groupStorer = st

	in := foldInput()
	in.SourceType = "assemblee_law_page"
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{in})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if len(st.queries) != 0 || judge.calls != 0 || len(st.merges) != 0 || res.Inserted != 1 || stance.calls != 1 {
		t.Fatalf("a vote must not be grouped: queries=%d calls=%d merges=%v res=%+v stance calls=%d", len(st.queries), judge.calls, st.merges, res, stance.calls)
	}
}
