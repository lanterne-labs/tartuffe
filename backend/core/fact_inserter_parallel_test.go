package core

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"
)

// lockedInserterStorer is a Storer safe for concurrent use: one person id per
// subject name.
type lockedInserterStorer struct {
	mu      sync.Mutex
	persons map[string]int64
	nextID  int64
}

func (s *lockedInserterStorer) FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error) {
	id, err := s.FindOrInsertPersonWithType(ctx, names[0], "")
	return []int64{id}, err
}

func (s *lockedInserterStorer) FindOrInsertPersonWithType(ctx context.Context, name, entityType string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.persons[name]; ok {
		return id, nil
	}
	s.nextID++
	s.persons[name] = s.nextID
	return s.nextID, nil
}

func (s *lockedInserterStorer) FindSimilarActiveFacts(ctx context.Context, q SimilarFactQuery) ([]SimilarFact, error) {
	return nil, nil
}

func (s *lockedInserterStorer) InsertFact(ctx context.Context, input StoreFactInput) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return s.nextID, nil
}

func (s *lockedInserterStorer) DeleteFact(ctx context.Context, factID int64) error { return nil }

func (s *lockedInserterStorer) LinkSourceToFact(ctx context.Context, factID int64, sourceURL, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (int64, error) {
	return factID, nil
}

func (s *lockedInserterStorer) LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error {
	return nil
}

// orderingStancer records the facts each person's stance routing saw, and
// the most routings in flight at once.
type orderingStancer struct {
	mu       sync.Mutex
	order    map[int64][]string
	inFlight int
	peak     int
}

func (o *orderingStancer) ProcessNewFact(ctx context.Context, personID, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	o.mu.Lock()
	o.inFlight++
	o.peak = max(o.peak, o.inFlight)
	o.order[personID] = append(o.order[personID], factText)
	o.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	o.mu.Lock()
	o.inFlight--
	o.mu.Unlock()
	return &StanceProcessResult{Verdict: "NEW_STANCE", StanceID: 1}, nil
}

// TestInsertFactsConcurrency: with an endpoint that serves 4 calls at once,
// the subjects run 4 at a time while each subject's facts keep their order;
// on the local profile (1 call), one fact at a time.
func TestInsertFactsConcurrency(t *testing.T) {
	var facts []StoreFactInput
	for p := 0; p < 8; p++ {
		for k := 0; k < 3; k++ {
			facts = append(facts, StoreFactInput{
				SubjectEntity: fmt.Sprintf("voter %d", p), NormalizedFactText: fmt.Sprintf("p%d-f%d", p, k),
				SourceType: "assemblee_law_page",
			})
		}
	}
	run := func() (*InsertResult, *orderingStancer, *lockedInserterStorer) {
		st := &lockedInserterStorer{persons: map[string]int64{}}
		stancer := &orderingStancer{order: map[int64][]string{}}
		res, err := NewFactInserter(st, stancer).InsertFacts(context.Background(), facts)
		if err != nil {
			t.Fatal(err)
		}
		return res, stancer, st
	}

	res, stancer, _ := run()
	if res.Inserted != len(facts) || stancer.peak != 1 {
		t.Fatalf("local profile: inserted %d, peak %d; want %d and 1", res.Inserted, stancer.peak, len(facts))
	}

	restore, err := llm.SetRouting(llm.ClaudeRouting(llm.EndpointClaudeTeam, &llm.ClaudeTeamEndpoint{MaxProcs: 4}, "claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	res, stancer, st := run()
	if res.Inserted != len(facts) || len(res.InsertedMeta) != len(facts) {
		t.Fatalf("inserted %d (meta %d), want %d", res.Inserted, len(res.InsertedMeta), len(facts))
	}
	if stancer.peak < 2 || stancer.peak > 4 {
		t.Errorf("peak in flight = %d, want 2..4", stancer.peak)
	}
	for p := 0; p < 8; p++ {
		want := []string{fmt.Sprintf("p%d-f0", p), fmt.Sprintf("p%d-f1", p), fmt.Sprintf("p%d-f2", p)}
		if got := stancer.order[st.persons[fmt.Sprintf("voter %d", p)]]; !reflect.DeepEqual(got, want) {
			t.Errorf("voter %d saw %v, want %v in order", p, got, want)
		}
	}
}
