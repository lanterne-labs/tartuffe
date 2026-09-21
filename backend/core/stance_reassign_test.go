package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"digital-mirror/backend/llm"
)

// reassignStorer is the mock stance storer plus the merge and listing calls
// the singleton sweep needs, with an in-memory MergeStancesForPerson.
type reassignStorer struct {
	*mockStanceStorer
	factsByID map[int64]*FactForDedup
	merges    [][2]int64 // (src, dst)
}

func (r *reassignStorer) MergeStancesForPerson(ctx context.Context, srcID, dstID int64, newDstCentroid []float32) error {
	src, ok := r.stances[srcID]
	if !ok {
		return fmt.Errorf("no stance %d", srcID)
	}
	dst := r.stances[dstID]
	r.facts[dstID] = append(r.facts[dstID], r.facts[srcID]...)
	dst.FactCount += src.FactCount
	dst.ClusterEmbedding = newDstCentroid
	r.stances[dstID] = dst
	delete(r.stances, srcID)
	delete(r.facts, srcID)
	r.merges = append(r.merges, [2]int64{srcID, dstID})
	return nil
}

func (r *reassignStorer) UpdateStanceSummary(ctx context.Context, stanceID int64, summary, clusterLabel string) error {
	return nil
}

func (r *reassignStorer) ListPersonsNeedingMergeSweep(ctx context.Context, rulebookKey string) ([]PersonRecord, error) {
	return nil, nil
}

func (r *reassignStorer) RecordMergeSweep(ctx context.Context, personID int64, rulebookKey string) error {
	return nil
}

func (r *reassignStorer) ListPersonsWithSingletonStances(ctx context.Context) ([]PersonRecord, error) {
	return []PersonRecord{{ID: 1, Name: "p"}}, nil
}

func (r *reassignStorer) GetFactByID(ctx context.Context, factID int64) (*FactForDedup, error) {
	f, ok := r.factsByID[factID]
	if !ok {
		return nil, fmt.Errorf("no fact %d", factID)
	}
	return f, nil
}

// reassignLLMStub answers each prompt kind the sweep issues: the topic
// assignment (joins when the fact mentions "retraite", otherwise a new
// topic), the analyze-in-stance call (NEW_INFO, no contradictions) and the
// metadata refresh.
func reassignLLMStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		prompt := req.Messages[len(req.Messages)-1].Content
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()

		var out any
		switch {
		case strings.Contains(prompt, "You are a topic classifier"):
			idx := 0
			if strings.Contains(prompt, "NEW FACT:\nretraite") {
				// Candidates are numbered in shortlist order; find the retraites one.
				for i, line := range strings.Split(prompt[strings.Index(prompt, "EXISTING TOPICS:"):], "\n")[1:] {
					if strings.Contains(line, `"retraites`) {
						idx = i + 1
						break
					}
				}
			}
			out = map[string]any{"reasoning": "stub", "assigned_topic_index": idx, "new_topic_label": "autre sujet"}
		case strings.Contains(prompt, "KEY FACTS IN THIS STANCE"):
			out = map[string]any{"reasoning": "stub", "primary_verdict": "NEW_INFO", "contradictions": []any{}}
		default:
			out = map[string]string{"label": "retraites (refreshed)", "summary": "refreshed"}
		}
		content, _ := json.Marshal(out)
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": string(content)}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &prompts
}

func TestReassignSingletonsForPerson(t *testing.T) {
	srv, prompts := reassignLLMStub(t)
	mock := newMockStanceStorer()
	store := &reassignStorer{mockStanceStorer: mock, factsByID: map[int64]*FactForDedup{}}
	emb := []float32{1, 0}
	add := func(label string, factIDs ...int64) int64 {
		id, _ := mock.InsertStanceDocument(context.Background(), StanceDocument{PersonID: 1, ClusterLabel: label, Summary: "s " + label, ClusterEmbedding: emb, FactCount: len(factIDs)})
		for _, f := range factIDs {
			mock.facts[id] = append(mock.facts[id], StanceFactInfo{ID: f, NormalizedFactText: fmt.Sprintf("fact %d", f)})
			store.factsByID[f] = &FactForDedup{ID: f, NormalizedFactText: fmt.Sprintf("fact %d", f), Embedding: emb}
		}
		return id
	}
	big := add("retraites", 10, 11, 12)
	joins := add("âge de départ", 20)
	store.factsByID[20].NormalizedFactText = "retraite à 62 ans"
	kept := add("nucléaire", 30)

	cfg := llm.Config{BaseURL: srv.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	sm := &StanceManager{storer: store, client: cfg.NewClient("reassign-test")}

	st, err := ReassignSingletonsForPerson(context.Background(), store, sm, PersonRecord{ID: 1, Name: "p"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Singletons != 2 || st.Joined != 1 || st.Kept != 1 || st.Failures != 0 {
		t.Fatalf("stats = %+v, want 2 singletons, 1 joined, 1 kept", st)
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{joins, big} {
		t.Fatalf("merges = %v, want [[%d %d]]", store.merges, joins, big)
	}
	if _, live := store.stances[joins]; live {
		t.Fatalf("stance %d should be gone after joining %d", joins, big)
	}
	if _, live := store.stances[kept]; !live {
		t.Fatalf("stance %d (kept) should still exist", kept)
	}
	if n := len(store.facts[big]); n != 4 {
		t.Fatalf("stance %d has %d facts, want 4", big, n)
	}
	// The singleton itself was never offered as a destination.
	for _, p := range *prompts {
		if strings.Contains(p, "You are a topic classifier") && strings.Contains(p, `"âge de départ"`) {
			t.Fatalf("the singleton was offered as a candidate:\n%s", p)
		}
	}
	// Joined: analyze against the destination's facts (majority-voted, so
	// more than one call), then a metadata refresh.
	var analyzed, refreshed int
	for _, p := range *prompts {
		if strings.Contains(p, "KEY FACTS IN THIS STANCE") {
			analyzed++
		} else if !strings.Contains(p, "You are a topic classifier") {
			refreshed++
		}
	}
	if analyzed < 1 || refreshed < 1 {
		t.Fatalf("analyze calls = %d, refresh calls = %d, want both >= 1", analyzed, refreshed)
	}

	// Dry run: decisions only, nothing merged.
	store.merges = nil
	add("retraite anticipée", 40)
	store.factsByID[40].NormalizedFactText = "retraite anticipée pour les carrières longues"
	st, err = ReassignSingletonsForPerson(context.Background(), store, sm, PersonRecord{ID: 1, Name: "p"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Joined != 1 || len(store.merges) != 0 {
		t.Fatalf("dry run: stats = %+v merges = %v, want 1 join decided and no merge", st, store.merges)
	}
}

type placementStorer struct {
	*reassignStorer
	placements []FactPlacement
}

func (p *placementStorer) SampleFactsInMultiFactStances(ctx context.Context, personIDs []int64, n int) ([]FactPlacement, error) {
	return p.placements, nil
}

// The placement check re-assigns among ALL stances with the fact hidden from
// the evidence, and counts stays / moves / splits without writing anything.
func TestCheckFactPlacements(t *testing.T) {
	srv, prompts := reassignLLMStub(t)
	mock := newMockStanceStorer()
	base := &reassignStorer{mockStanceStorer: mock, factsByID: map[int64]*FactForDedup{}}
	emb := []float32{1, 0}
	add := func(label string, factIDs ...int64) int64 {
		id, _ := mock.InsertStanceDocument(context.Background(), StanceDocument{PersonID: 1, ClusterLabel: label, Summary: "s " + label, ClusterEmbedding: emb, FactCount: len(factIDs)})
		for _, f := range factIDs {
			mock.facts[id] = append(mock.facts[id], StanceFactInfo{ID: f, NormalizedFactText: fmt.Sprintf("fact %d", f)})
			base.factsByID[f] = &FactForDedup{ID: f, NormalizedFactText: fmt.Sprintf("fact %d", f), Embedding: emb}
		}
		return id
	}
	retraites := add("retraites", 10, 11)
	other := add("nucléaire", 20, 21)
	base.factsByID[10].NormalizedFactText = "retraite à 62 ans"  // stays: the stub routes "retraite" to retraites
	base.factsByID[20].NormalizedFactText = "retraite anticipée" // move: sits in nucléaire, routed to retraites
	// fact 21 ("fact 21"): the stub answers new topic → split
	store := &placementStorer{reassignStorer: base, placements: []FactPlacement{
		{FactID: 10, StanceID: retraites, StanceLabel: "retraites", PersonID: 1, PersonName: "p"},
		{FactID: 20, StanceID: other, StanceLabel: "nucléaire", PersonID: 1, PersonName: "p"},
		{FactID: 21, StanceID: other, StanceLabel: "nucléaire", PersonID: 1, PersonName: "p"},
	}}
	cfg := llm.Config{BaseURL: srv.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	sm := &StanceManager{storer: store, client: cfg.NewClient("placement-test")}

	st, err := CheckFactPlacements(context.Background(), store, sm, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sampled != 3 || st.Stays != 1 || st.Moves != 1 || st.Splits != 1 || st.Failures != 0 {
		t.Fatalf("stats = %+v, want 1 stay, 1 move, 1 split", st)
	}
	if len(store.merges) != 0 || len(store.facts[retraites]) != 2 {
		t.Fatal("the placement check must not write")
	}
	if n := len(*prompts); n != 3 {
		t.Fatalf("%d LLM calls, want one assignment per sampled fact", n)
	}
}
