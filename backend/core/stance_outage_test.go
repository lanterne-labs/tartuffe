package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"
)

// outageLLM stands in for the LLM with the primary endpoint down (breaker
// open) and the stub as the fallback: a prompt answer returns 200 with its
// JSON, a nil answer returns 402, so the call fails with llm.ErrUnavailable
// at once, as when the local LLM is stopped mid-run and the fallback has no
// credits.
type outageLLM struct {
	mu      sync.Mutex
	prompts []string
}

func (o *outageLLM) count(substr string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, p := range o.prompts {
		if strings.Contains(p, substr) {
			n++
		}
	}
	return n
}

const (
	topicPrompt   = "You are a topic classifier"
	analyzePrompt = "KEY FACTS IN THIS STANCE"
	verifyPrompt  = "You are auditing a *claimed* contradiction"
)

func newOutageManager(t *testing.T, storer StanceStorer, answer func(prompt string) any) (*StanceManager, *outageLLM) {
	t.Helper()
	o := &outageLLM{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		prompt := req.Messages[len(req.Messages)-1].Content
		o.mu.Lock()
		o.prompts = append(o.prompts, prompt)
		o.mu.Unlock()

		out := answer(prompt)
		if out == nil {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits"}}`))
			return
		}
		content, ok := out.(string)
		if !ok {
			b, _ := json.Marshal(out)
			content = string(b)
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("FALLBACK_URL", srv.URL+"/v1")
	t.Setenv("FALLBACK_CHAT_MODEL", "stub")
	t.Setenv("FALLBACK_KEY", "k")
	llm.MarkPrimaryDown("outage test")
	t.Cleanup(llm.MarkPrimaryUp)

	cfg := llm.Config{BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", ChatModel: "primary"}
	return &StanceManager{storer: storer, client: cfg.NewClient("outage-test")}, o
}

// answers is the stub's happy path; a test overrides the prompt kind that
// goes unavailable with nil.
func answers(overrides map[string]any) func(string) any {
	return func(prompt string) any {
		for kind, out := range overrides {
			if strings.Contains(prompt, kind) {
				return out
			}
		}
		switch {
		case strings.Contains(prompt, topicPrompt):
			return map[string]any{"reasoning": "stub", "assigned_topic_index": 1, "new_topic_label": ""}
		case strings.Contains(prompt, analyzePrompt):
			return map[string]any{"reasoning": "stub", "primary_verdict": "CONTRADICTION",
				"contradictions": []map[string]any{{"key_fact_id": 10, "reason": "62 vs 64"}}}
		case strings.Contains(prompt, verifyPrompt):
			return "REASON: real reversal.\nVERDICT: YES"
		default:
			return map[string]string{"label": "retraites", "summary": "refreshed"}
		}
	}
}

// pensionStorer holds person 1's stance 1 with one key fact (10).
func pensionStorer() *mockStanceStorer {
	m := newMockStanceStorer()
	m.stances[1] = StanceDocument{ID: 1, PersonID: 1, ClusterLabel: "retraites", Summary: "âge de départ", ClusterEmbedding: []float32{1, 0}, KeyFactIDs: []int64{10}, FactCount: 1}
	m.facts[1] = []StanceFactInfo{{ID: 10, NormalizedFactText: "veut la retraite à 62 ans"}}
	m.nextID = 2
	return m
}

// TestProcessNewFact_UnavailableLLMWritesNothing: when the LLM goes away
// during the analysis or its verification, ProcessNewFact fails with
// ErrUnavailable and writes nothing — the fact is not linked to the stance
// (so its (person, fact) pair stays pending) and no contradiction is
// recorded. The control case proves the same flow links and records once
// every call succeeds.
func TestProcessNewFact_UnavailableLLMWritesNothing(t *testing.T) {
	for _, down := range []string{analyzePrompt, verifyPrompt} {
		t.Run("unavailable at "+down, func(t *testing.T) {
			storer := pensionStorer()
			sm, _ := newOutageManager(t, storer, answers(map[string]any{down: nil}))
			_, err := sm.ProcessNewFact(context.Background(), 1, 20, "veut la retraite à 64 ans", []float32{1, 0}, "declared")
			if !errors.Is(err, llm.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if got := storer.facts[1]; len(got) != 1 || got[0].ID != 10 {
				t.Errorf("stance 1 facts = %+v, want only fact 10: the failed fact must stay pending", got)
			}
			if len(storer.contradictions) != 0 || storer.stances[1].FactCount != 1 {
				t.Errorf("nothing may be recorded: contradictions=%v fact_count=%d", storer.contradictions, storer.stances[1].FactCount)
			}
		})
	}

	t.Run("control: every call succeeds", func(t *testing.T) {
		storer := pensionStorer()
		sm, _ := newOutageManager(t, storer, answers(nil))
		res, err := sm.ProcessNewFact(context.Background(), 1, 20, "veut la retraite à 64 ans", []float32{1, 0}, "declared")
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != "CONTRADICTION" || len(storer.contradictions) != 1 || len(storer.facts[1]) != 2 {
			t.Fatalf("verdict=%s contradictions=%v facts=%+v", res.Verdict, storer.contradictions, storer.facts[1])
		}
	})
}

// fanoutStorer lists person 1 with two orphan facts.
type fanoutStorer struct{}

func (fanoutStorer) ListPersonsWithOrphanFacts(context.Context) ([]PersonRecord, error) {
	return []PersonRecord{{ID: 1, Name: "p"}}, nil
}

func (fanoutStorer) ListOrphanFactsForPerson(context.Context, int64) ([]StanceRoutingFact, error) {
	return []StanceRoutingFact{
		{ID: 20, FactText: "veut la retraite à 64 ans", Embedding: []float32{1, 0}},
		{ID: 21, FactText: "veut la retraite à 65 ans", Embedding: []float32{1, 0}},
	}, nil
}

// TestAssignOrphans_StopsAtUnavailableLLM: the orphan routing stops at the
// first routing the LLM could not serve, returns ErrUnavailable, leaves that
// fact unlinked and does not walk the remaining facts or rebundle.
func TestAssignOrphans_StopsAtUnavailableLLM(t *testing.T) {
	storer := pensionStorer()
	sm, o := newOutageManager(t, storer, answers(map[string]any{analyzePrompt: nil}))
	rebundled := false
	rebundle := func(context.Context, PersonRecord) (int64, int64, int64) { rebundled = true; return 0, 0, 0 }

	person := PersonRecord{ID: 1, Name: "p"}
	ps, err := AssignOrphansForPerson(context.Background(), fanoutStorer{}, sm, rebundle, person, false, nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if ps.OrphansAssigned != 0 || ps.OrphanFailures != 0 {
		t.Errorf("stats = %+v, want nothing counted: the interrupted routing is not a failure", ps)
	}
	if n := o.count(topicPrompt); n != 1 {
		t.Errorf("topic assignments = %d, want 1: fact 21 must not be routed after the outage", n)
	}
	if len(storer.facts[1]) != 1 || rebundled {
		t.Errorf("facts=%+v rebundled=%v: nothing may be written after the outage", storer.facts[1], rebundled)
	}
}

// TestAssignOrphans_StopsAtDeadline: an expired context routes nothing and
// returns the deadline; a run cancelled under a routing stops there without
// charging the fact as a failure or routing the next one.
func TestAssignOrphans_StopsAtDeadline(t *testing.T) {
	storer := pensionStorer()
	ctx, cut := context.WithCancel(context.Background())
	defer cut()
	sm, o := newOutageManager(t, storer, func(prompt string) any {
		if strings.Contains(prompt, topicPrompt) {
			cut()
		}
		return answers(nil)(prompt)
	})
	rebundle := func(context.Context, PersonRecord) (int64, int64, int64) { return 0, 0, 0 }

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	person := PersonRecord{ID: 1, Name: "p"}
	ps, err := AssignOrphansForPerson(expired, fanoutStorer{}, sm, rebundle, person, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) || ps.OrphansAssigned != 0 || o.count(topicPrompt) != 0 {
		t.Fatalf("stats=%+v err=%v topic calls=%d; want nothing routed and the deadline", ps, err, o.count(topicPrompt))
	}

	ps, err = AssignOrphansForPerson(ctx, fanoutStorer{}, sm, rebundle, person, false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if ps.OrphansAssigned != 0 || ps.OrphanFailures != 0 || o.count(topicPrompt) != 1 || len(storer.facts[1]) != 1 {
		t.Errorf("stats=%+v topic calls=%d facts=%+v; want the cut routing uncounted and fact 21 not routed", ps, o.count(topicPrompt), storer.facts[1])
	}
}

// TestApplyMergeSubgroup_AnalysisBeforeMerge: the post-merge analysis runs
// before the merge commits, so an LLM outage during it leaves both stances
// unmerged (the subgroup is proposed again next sweep) instead of deleting
// the source with its facts' cross-stance check lost. Any other analysis
// failure does not block the merge: that fact is merged unchecked and
// counted. An outage at the final label refresh is returned with the
// committed merge's counts.
func TestApplyMergeSubgroup_AnalysisBeforeMerge(t *testing.T) {
	newStorer := func() *reassignStorer {
		m := pensionStorer()
		m.stances[2] = StanceDocument{ID: 2, PersonID: 1, ClusterLabel: "âge de la retraite", ClusterEmbedding: []float32{1, 0}, FactCount: 1}
		m.facts[2] = []StanceFactInfo{{ID: 20, NormalizedFactText: "veut la retraite à 64 ans"}}
		return &reassignStorer{mockStanceStorer: m}
	}
	sub := MergeSubgroup{StanceIDs: []int64{1, 2}, Label: "retraites", Summary: "âge de départ"}

	t.Run("unavailable", func(t *testing.T) {
		storer := newStorer()
		sm, _ := newOutageManager(t, storer, answers(map[string]any{verifyPrompt: nil}))
		byID := map[int64]StanceDocument{1: storer.stances[1], 2: storer.stances[2]}
		merged, n, _, err := applyMergeSubgroup(context.Background(), storer, sm, 1, byID, sub)
		if !errors.Is(err, llm.ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
		if merged != 0 || n != 0 || len(storer.merges) != 0 || len(storer.contradictions) != 0 {
			t.Errorf("merged=%d contradictions=%d merges=%v: nothing may be merged or recorded", merged, n, storer.merges)
		}
	})

	t.Run("analysis rejected", func(t *testing.T) {
		storer := newStorer()
		sm, _ := newOutageManager(t, storer, answers(map[string]any{analyzePrompt: "not json"}))
		byID := map[int64]StanceDocument{1: storer.stances[1], 2: storer.stances[2]}
		merged, n, failures, err := applyMergeSubgroup(context.Background(), storer, sm, 1, byID, sub)
		if err != nil {
			t.Fatal(err)
		}
		if merged != 1 || n != 0 || failures != 1 || len(storer.contradictions) != 0 {
			t.Errorf("merged=%d contradictions=%d failures=%d: the merge must go on with the fact unchecked", merged, n, failures)
		}
	})

	t.Run("refresh unavailable", func(t *testing.T) {
		storer := newStorer()
		judged := answers(nil)
		sm, _ := newOutageManager(t, storer, func(prompt string) any {
			for _, kind := range []string{topicPrompt, analyzePrompt, verifyPrompt} {
				if strings.Contains(prompt, kind) {
					return judged(prompt)
				}
			}
			return nil // the label + summary refresh
		})
		byID := map[int64]StanceDocument{1: storer.stances[1], 2: storer.stances[2]}
		merged, n, _, err := applyMergeSubgroup(context.Background(), storer, sm, 1, byID, sub)
		if !errors.Is(err, llm.ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
		if merged != 1 || n != 1 || len(storer.merges) != 1 {
			t.Errorf("merged=%d contradictions=%d merges=%v: the committed merge is reported", merged, n, storer.merges)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		// Run 128 (2026-10-17): the phase deadline cut the analysis. Each
		// fact after it failed too, was counted "merged unchecked", and its
		// call was retried. The cut must stop the subgroup: no merge, no
		// failure counted, no retry.
		storer := newStorer()
		ctx, cut := context.WithCancel(context.Background())
		defer cut()
		sm, o := newOutageManager(t, storer, func(prompt string) any {
			if strings.Contains(prompt, analyzePrompt) {
				cut()
			}
			return answers(nil)(prompt)
		})
		byID := map[int64]StanceDocument{1: storer.stances[1], 2: storer.stances[2]}
		merged, n, failures, err := applyMergeSubgroup(ctx, storer, sm, 1, byID, sub)
		if err == nil {
			t.Fatal("want the error of the cut analysis")
		}
		if merged != 0 || n != 0 || failures != 0 || len(storer.merges) != 0 {
			t.Errorf("merged=%d contradictions=%d failures=%d merges=%v: a cut merges nothing and counts no failure", merged, n, failures, storer.merges)
		}
		if got := o.count(analyzePrompt); got != 1 {
			t.Errorf("analyze calls = %d, want 1: no retry after the cut", got)
		}
	})

	t.Run("control", func(t *testing.T) {
		storer := newStorer()
		sm, _ := newOutageManager(t, storer, answers(nil))
		byID := map[int64]StanceDocument{1: storer.stances[1], 2: storer.stances[2]}
		merged, n, _, err := applyMergeSubgroup(context.Background(), storer, sm, 1, byID, sub)
		if err != nil {
			t.Fatal(err)
		}
		if merged != 1 || n != 1 || len(storer.contradictions) != 1 {
			t.Errorf("merged=%d contradictions=%d recorded=%v", merged, n, storer.contradictions)
		}
	})
}

// sweepStorer lists persons 1 and 2 for the merge sweep and records memos.
type sweepStorer struct {
	*reassignStorer
	memos []int64
}

func (s *sweepStorer) ListPersonsNeedingMergeSweep(context.Context, string) ([]PersonRecord, error) {
	return []PersonRecord{{ID: 1, Name: "p"}, {ID: 2, Name: "q"}}, nil
}

func (s *sweepStorer) RecordMergeSweep(_ context.Context, personID int64, _ string) error {
	s.memos = append(s.memos, personID)
	return nil
}

// TestStanceMergeSweep_StopsAtDeadline: an expired context judges no person;
// a sweep cancelled under a person stops there, neither memoizing it nor
// going on to the next person.
func TestStanceMergeSweep_StopsAtDeadline(t *testing.T) {
	m := pensionStorer()
	m.stances[2] = StanceDocument{ID: 2, PersonID: 1, ClusterLabel: "âge de la retraite", ClusterEmbedding: []float32{1, 0}, FactCount: 1}
	m.facts[2] = []StanceFactInfo{{ID: 20, NormalizedFactText: "veut la retraite à 64 ans"}}
	storer := &sweepStorer{reassignStorer: &reassignStorer{mockStanceStorer: m}}
	ctx, cut := context.WithCancel(context.Background())
	defer cut()
	sm, o := newOutageManager(t, storer, func(prompt string) any {
		cut()
		return answers(nil)(prompt)
	})

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	stats, err := RunStanceMergeSweep(expired, storer, sm, DefaultStanceMergeOptions)
	if !errors.Is(err, context.DeadlineExceeded) || stats.PersonsProcessed != 0 || len(o.prompts) != 0 || len(storer.memos) != 0 {
		t.Fatalf("stats=%+v err=%v prompts=%d memos=%v; want nothing judged and the deadline", stats, err, len(o.prompts), storer.memos)
	}

	stats, err = RunStanceMergeSweep(ctx, storer, sm, DefaultStanceMergeOptions)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if stats.PersonsProcessed != 1 || len(storer.memos) != 0 || len(storer.merges) != 0 {
		t.Errorf("stats=%+v memos=%v merges=%v; want the sweep stopped at person 1, unmemoized", stats, storer.memos, storer.merges)
	}
}

// failingMergeStorer fails the merge of one source stance.
type failingMergeStorer struct {
	*reassignStorer
	failSrc int64
}

func (f *failingMergeStorer) MergeStancesForPerson(ctx context.Context, srcID, dstID int64, newDstCentroid []float32) error {
	if srcID == f.failSrc {
		return errors.New("merge failed")
	}
	return f.reassignStorer.MergeStancesForPerson(ctx, srcID, dstID, newDstCentroid)
}

// TestApplyMergeSubgroup_RecordsOnlyWithinMergedStance: when one source's
// merge fails, no contradiction is recorded for its facts, nor between a
// merged fact and one of its facts: only pairs now inside dst.
func TestApplyMergeSubgroup_RecordsOnlyWithinMergedStance(t *testing.T) {
	m := pensionStorer()
	m.stances[2] = StanceDocument{ID: 2, PersonID: 1, ClusterLabel: "âge de la retraite", ClusterEmbedding: []float32{1, 0}, FactCount: 1}
	m.facts[2] = []StanceFactInfo{{ID: 20, NormalizedFactText: "veut la retraite à 64 ans"}}
	m.stances[3] = StanceDocument{ID: 3, PersonID: 1, ClusterLabel: "report de l'âge légal", ClusterEmbedding: []float32{1, 0}, FactCount: 1}
	m.facts[3] = []StanceFactInfo{{ID: 30, NormalizedFactText: "veut la retraite à 65 ans"}}
	storer := &failingMergeStorer{reassignStorer: &reassignStorer{mockStanceStorer: m}, failSrc: 3}

	// Every analysis claims a contradiction with facts 10, 20 and 30.
	sm, _ := newOutageManager(t, storer, answers(map[string]any{analyzePrompt: map[string]any{
		"reasoning": "stub", "primary_verdict": "CONTRADICTION",
		"contradictions": []map[string]any{{"key_fact_id": 10, "reason": "a"}, {"key_fact_id": 20, "reason": "b"}, {"key_fact_id": 30, "reason": "c"}},
	}}))
	byID := map[int64]StanceDocument{1: m.stances[1], 2: m.stances[2], 3: m.stances[3]}
	sub := MergeSubgroup{StanceIDs: []int64{1, 2, 3}, Label: "retraites", Summary: "âge de départ"}
	merged, n, _, err := applyMergeSubgroup(context.Background(), storer, sm, 1, byID, sub)
	if err != nil {
		t.Fatal(err)
	}
	if merged != 1 || n != 1 {
		t.Fatalf("merged=%d contradictions=%d, want 1 and 1", merged, n)
	}
	if c := m.contradictions[0]; c.factAID != 20 || c.factBID != 10 {
		t.Errorf("recorded %+v, want only the pair (20, 10) inside dst", m.contradictions)
	}
}

// TestProcessNewFact_RefreshUnavailableKeepsTheLink: an LLM outage at the
// label + summary refresh comes after the fact was committed to its stance
// (membership, contradictions): ProcessNewFact returns the outage marked
// ErrStanceRefreshPending, the link stays, and the fact stays pending for
// the stance's next refresh.
func TestProcessNewFact_RefreshUnavailableKeepsTheLink(t *testing.T) {
	storer := pensionStorer()
	judged := answers(nil)
	sm, _ := newOutageManager(t, storer, func(prompt string) any {
		for _, kind := range []string{topicPrompt, analyzePrompt, verifyPrompt} {
			if strings.Contains(prompt, kind) {
				return judged(prompt)
			}
		}
		return nil // the label + summary refresh
	})
	_, err := sm.ProcessNewFact(context.Background(), 1, 20, "veut la retraite à 64 ans", []float32{1, 0}, "declared")
	if !errors.Is(err, llm.ErrUnavailable) || !errors.Is(err, ErrStanceRefreshPending) {
		t.Fatalf("err = %v, want ErrUnavailable marked ErrStanceRefreshPending", err)
	}
	if len(storer.facts[1]) != 2 || len(storer.contradictions) != 1 {
		t.Errorf("facts=%+v contradictions=%v: the committed link and contradiction stay", storer.facts[1], storer.contradictions)
	}
	if storer.stances[1].Summary != "âge de départ" {
		t.Errorf("summary = %q, want the previous one until the next refresh", storer.stances[1].Summary)
	}
}

// stanceErrProcessor fails every routing with err.
type stanceErrProcessor struct{ err error }

func (p stanceErrProcessor) ProcessNewFact(context.Context, int64, int64, string, []float32, string) (*StanceProcessResult, error) {
	return nil, p.err
}

// TestInsertFacts_KeepsFactLinkedBeforeRefreshOutage: a routing failure rolls
// the fact back, except when the fact is already in its stance and only the
// refresh is pending; either way the error reaches the caller.
func TestInsertFacts_KeepsFactLinkedBeforeRefreshOutage(t *testing.T) {
	input := StoreFactInput{SubjectEntity: "p", NormalizedFactText: "veut la retraite à 64 ans", SourceType: "assemblee_law_page"}
	for name, tc := range map[string]struct {
		err         error
		wantDeleted int
	}{
		"refresh pending": {fmt.Errorf("%w: stance 1: %w", ErrStanceRefreshPending, llm.ErrUnavailable), 0},
		"routing failed":  {fmt.Errorf("analyze: %w", llm.ErrUnavailable), 1},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeInserterStorer{}
			_, err := NewFactInserter(st, stanceErrProcessor{tc.err}).InsertFacts(context.Background(), []StoreFactInput{input})
			if !errors.Is(err, llm.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if st.deleted != tc.wantDeleted {
				t.Errorf("deleted %d facts, want %d", st.deleted, tc.wantDeleted)
			}
		})
	}
}

// TestInsertFacts_RollsBackFactCutByDeadline: a fact whose stance routing
// is cut by the run deadline is rolled back although the context is done, so
// a resumed run does not count it as stored.
func TestInsertFacts_RollsBackFactCutByDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	input := StoreFactInput{SubjectEntity: "p", NormalizedFactText: "veut la retraite à 64 ans", SourceType: "assemblee_law_page"}
	st := &fakeInserterStorer{}
	_, err := NewFactInserter(st, stanceErrProcessor{ctx.Err()}).InsertFacts(ctx, []StoreFactInput{input})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if st.deleted != 1 {
		t.Errorf("deleted %d facts, want the cut fact rolled back", st.deleted)
	}
}

// TestProcessNewFact_LabelPassUnavailable: for a person with more stances
// than the embedding shortlist holds, an LLM outage in the topic label pass
// fails the routing instead of assigning on the shortlist alone, and writes
// nothing.
func TestProcessNewFact_LabelPassUnavailable(t *testing.T) {
	storer := pensionStorer()
	for id := int64(2); id <= shortlistTopK+1; id++ {
		storer.stances[id] = StanceDocument{ID: id, PersonID: 1, ClusterLabel: fmt.Sprintf("sujet %d", id), ClusterEmbedding: []float32{0, 1}, FactCount: 1}
	}
	storer.nextID = shortlistTopK + 2
	sm, o := newOutageManager(t, storer, answers(map[string]any{"(labels only)": nil}))
	_, err := sm.ProcessNewFact(context.Background(), 1, 20, "veut la retraite à 64 ans", []float32{1, 0}, "declared")
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if n := o.count("decide which topic the fact belongs to"); n != 0 {
		t.Errorf("assignment calls = %d, want 0 after the label pass outage", n)
	}
	if len(storer.stances) != shortlistTopK+1 || len(storer.facts[1]) != 1 {
		t.Errorf("stances=%d facts=%+v: nothing may be written", len(storer.stances), storer.facts[1])
	}
}

// TestAnalyzeAcrossBatches_InBatchOrder: the result lists the contradictions
// in batch order, whatever the order in which the batches finish. The batches
// run as many at once as the endpoint serves (llm.ForEach): one at a time on
// the local endpoint of this test.
func TestAnalyzeAcrossBatches_InBatchOrder(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	sm, o := newOutageManager(t, newMockStanceStorer(), func(prompt string) any {
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		// The first key fact of the batch: the first batch answers last.
		var first int64
		facts := prompt[strings.Index(prompt, "\n- [ID "):]
		_, _ = fmt.Sscanf(facts, "\n- [ID %d]", &first)
		time.Sleep(time.Duration(200/max(first, 1)) * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return map[string]any{"reasoning": "stub", "primary_verdict": "CONTRADICTION",
			"contradictions": []map[string]any{{"key_fact_id": first, "reason": "r"}}}
	})
	var facts []StanceFactInfo
	for id := int64(1); id <= 3*keyFactBatchSize; id++ {
		facts = append(facts, StanceFactInfo{ID: id, NormalizedFactText: fmt.Sprintf("fait %d", id)})
	}
	agg, err := sm.analyzeAcrossBatches(context.Background(), "résumé", facts, "nouveau fait")
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, c := range agg.Contradictions {
		got = append(got, c.KeyFactID)
	}
	want := []int64{1, keyFactBatchSize + 1, 2*keyFactBatchSize + 1}
	if fmt.Sprint(got) != fmt.Sprint(want) || agg.PrimaryVerdict != "CONTRADICTION" {
		t.Errorf("contradictions = %v (%s), want %v in batch order", got, agg.PrimaryVerdict, want)
	}
	if maxInFlight != 1 {
		t.Errorf("%d batches at one time, want 1 on the local endpoint", maxInFlight)
	}
	if n := o.count(analyzePrompt); n != 2*len(want) {
		t.Errorf("analyze calls = %d, want 2 agreeing votes per batch", n)
	}
}

// TestVerifyEntries_NothingUnverified: a contradiction with a key fact the
// analysis was not shown is dropped (prod #1205 got the reason of another
// pair), and one whose verification fails goes to the detector's queue
// instead of being recorded.
func TestVerifyEntries_NothingUnverified(t *testing.T) {
	storer := newMockStanceStorer()
	sm, _ := newOutageManager(t, storer, answers(map[string]any{verifyPrompt: "no verdict here"}))
	keyTexts := map[int64]string{10: "Il défend la retraite à 62 ans."}
	entries := []StanceContradictionEntry{
		{KeyFactID: 10, Reason: "62 vs 64"},
		{KeyFactID: 99, Reason: "a fact the analysis was not shown"},
	}
	got, err := sm.verifyEntries(context.Background(), 7, 5, "Il vote la retraite à 64 ans.", keyTexts, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("survivors: %+v, want none", got)
	}
	if len(storer.queued) != 1 || storer.queued[0] != [2]int64{5, 10} {
		t.Errorf("queued for the detector: %v, want the unverified pair 5-10", storer.queued)
	}
}
