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

// refreshLLMStub is an OpenAI-compatible chat endpoint that records every
// prompt it receives and answers with a label + summary derived from the
// prompt, so tests can assert both WHAT was sent (full rebuild vs fold, which
// facts, which tensions) and how the outputs chain across chunks.
type refreshLLMStub struct {
	mu      sync.Mutex
	prompts []string
	server  *httptest.Server
}

func newRefreshLLMStub(t *testing.T) *refreshLLMStub {
	t.Helper()
	s := &refreshLLMStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		prompt := req.Messages[len(req.Messages)-1].Content
		s.mu.Lock()
		s.prompts = append(s.prompts, prompt)
		n := len(s.prompts)
		s.mu.Unlock()

		// The reply echoes the call number and whether the call was a fold on
		// top of an existing summary, so chunk chaining is observable on both
		// the label and the summary.
		kind := "full"
		if strings.Contains(prompt, "NEW FACTS JUST ADDED") {
			kind = "fold"
			if strings.Contains(prompt, "CURRENT SUMMARY: (none yet)") {
				kind = "fold-from-scratch"
			}
		}
		out := map[string]string{
			"label":   fmt.Sprintf("label-%d", n),
			"summary": fmt.Sprintf("summary-%d(%s)", n, kind),
		}
		content, _ := json.Marshal(out)
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": string(content)}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *refreshLLMStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

func newRefreshTestManager(t *testing.T, stub *refreshLLMStub, storer StanceStorer) *StanceManager {
	t.Helper()
	return &StanceManager{
		storer: storer,
		apiKey: "test",
		apiURL: stub.server.URL + "/v1/chat/completions",
		model:  "stub",
		client: llm.NewBaseLLMClient("stance-refresh-test"),
	}
}

func factOfSize(id int64, chars int) StanceFactInfo {
	return StanceFactInfo{ID: id, NormalizedFactText: strings.Repeat("x", chars), CanonicalVerb: "declared"}
}

// Live path, steady state: the stance was summarized before and only the new
// fact is pending. It is folded into the current label + summary in ONE call
// that carries the old summary, the new fact and the tension the analyze just
// verified — never the whole fact list, never non-contradicted key facts —
// and the cursor advances over exactly the folded fact.
func TestRefreshAfterVerdict_FoldsPendingFactWithLiveTension(t *testing.T) {
	stub := newRefreshLLMStub(t)
	storer := newMockStanceStorer()
	sm := newRefreshTestManager(t, stub, storer)

	stanceID, _ := storer.InsertStanceDocument(context.Background(), StanceDocument{
		PersonID: 1, ClusterLabel: "réforme des retraites", Summary: "Il a promis de ne pas toucher à l'âge légal.", FactCount: 3,
	})
	storer.facts[stanceID] = []StanceFactInfo{
		{ID: 11, NormalizedFactText: "a promis de ne jamais relever l'âge légal de départ", CanonicalVerb: "promised"},
		{ID: 12, NormalizedFactText: "a salué le dialogue social", CanonicalVerb: "said"},
		{ID: 13, NormalizedFactText: "a voté POUR le report de l'âge légal à 64 ans", CanonicalVerb: "voted"},
	}
	storer.unsummarized = storer.facts[stanceID][2:] // only the new fact is pending
	matched := storer.stances[stanceID]
	keyTexts := map[int64]string{11: "a promis de ne jamais relever l'âge légal de départ", 12: "a salué le dialogue social"}
	verified := []StanceContradictionEntry{{KeyFactID: 11, Reason: "il vote maintenant le report à 64 ans"}}

	if err := sm.refreshAfterVerdict(context.Background(), &matched, 13, verified, keyTexts); err != nil {
		t.Fatalf("refreshAfterVerdict: %v", err)
	}

	calls := stub.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one LLM call, got %d", len(calls))
	}
	p := calls[0]
	for _, want := range []string{
		"NEW FACTS JUST ADDED",
		"CURRENT LABEL: réforme des retraites",
		"CURRENT SUMMARY: Il a promis de ne pas toucher à l'âge légal.",
		"- (voted) a voté POUR le report de l'âge légal à 64 ans",
		"⚠ CONTRADICTS the earlier fact: « a promis de ne jamais relever l'âge légal de départ » — il vote maintenant le report à 64 ans",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("fold prompt missing %q\n--- prompt ---\n%s", want, p)
		}
	}
	if strings.Contains(p, "a salué le dialogue social") {
		t.Errorf("fold prompt must not inline non-contradicted key facts")
	}
	if strings.Contains(p, "FACTS IN THIS STANCE") {
		t.Errorf("live path must fold, not rebuild")
	}
	got := storer.stances[stanceID]
	if got.ClusterLabel != "label-1" || got.Summary != "summary-1(fold)" {
		t.Errorf("metadata not persisted from the fold reply: %+v", got)
	}
	if len(storer.consumed) != 1 || len(storer.consumed[0]) != 1 || storer.consumed[0][0] != 13 {
		t.Errorf("cursor must advance over exactly the folded fact, got %v", storer.consumed)
	}
}

// Live path, self-healing: a fact whose earlier fold failed is still pending
// and gets folded together with the new one (the tension recorded for the
// older fact comes from the storer, the new one's from the verdict).
func TestRefreshAfterVerdict_FoldsEarlierFailedFactToo(t *testing.T) {
	stub := newRefreshLLMStub(t)
	storer := newMockStanceStorer()
	sm := newRefreshTestManager(t, stub, storer)

	stanceID, _ := storer.InsertStanceDocument(context.Background(), StanceDocument{PersonID: 1, ClusterLabel: "l", Summary: "s", FactCount: 4})
	storer.facts[stanceID] = []StanceFactInfo{
		{ID: 1, NormalizedFactText: "old one"}, {ID: 2, NormalizedFactText: "old two"},
		{ID: 3, NormalizedFactText: "missed earlier", CanonicalVerb: "said"},
		{ID: 4, NormalizedFactText: "brand new", CanonicalVerb: "voted"},
	}
	storer.unsummarized = storer.facts[stanceID][2:]
	storer.tensions = map[int64][]StanceTension{3: {{FactID: 3, OtherFactText: "old one", Reason: "recorded earlier"}}}
	matched := storer.stances[stanceID]

	err := sm.refreshAfterVerdict(context.Background(), &matched, 4,
		[]StanceContradictionEntry{{KeyFactID: 2, Reason: "fresh"}}, map[int64]string{2: "old two"})
	if err != nil {
		t.Fatalf("refreshAfterVerdict: %v", err)
	}
	calls := stub.calls()
	if len(calls) != 1 {
		t.Fatalf("expected one fold call, got %d", len(calls))
	}
	for _, want := range []string{
		"- (said) missed earlier", "⚠ CONTRADICTS the earlier fact: « old one » — recorded earlier",
		"- (voted) brand new", "⚠ CONTRADICTS the earlier fact: « old two » — fresh",
	} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("fold prompt missing %q\n%s", want, calls[0])
		}
	}
	if len(storer.consumed) != 1 || len(storer.consumed[0]) != 2 {
		t.Errorf("cursor must cover both folded facts, got %v", storer.consumed)
	}
}

// Live path, compaction: every stanceCompactionEvery-th fact triggers a full
// rebuild (all facts inlined) when they fit the context; when they do not
// fit, the fold happens as usual.
func TestRefreshAfterVerdict_Compaction(t *testing.T) {
	stub := newRefreshLLMStub(t)
	storer := newMockStanceStorer()
	sm := newRefreshTestManager(t, stub, storer)

	stanceID, _ := storer.InsertStanceDocument(context.Background(), StanceDocument{
		PersonID: 1, ClusterLabel: "l", Summary: "s", FactCount: stanceCompactionEvery - 1,
	})
	for i := int64(1); i <= int64(stanceCompactionEvery); i++ {
		storer.facts[stanceID] = append(storer.facts[stanceID], StanceFactInfo{ID: i, NormalizedFactText: fmt.Sprintf("fact %d", i), CanonicalVerb: "said"})
	}
	storer.unsummarized = storer.facts[stanceID][stanceCompactionEvery-1:]
	matched := storer.stances[stanceID]

	if err := sm.refreshAfterVerdict(context.Background(), &matched, int64(stanceCompactionEvery), nil, nil); err != nil {
		t.Fatalf("refreshAfterVerdict: %v", err)
	}
	calls := stub.calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "FACTS IN THIS STANCE") || !strings.Contains(calls[0], "[ID 7] (said) fact 7") {
		t.Fatalf("compaction must be a full rebuild inlining every fact, got %d calls", len(calls))
	}
	if storer.stances[stanceID].Summary != "summary-1(full)" {
		t.Errorf("full-rebuild reply not persisted: %+v", storer.stances[stanceID])
	}
	if len(storer.consumed[0]) != stanceCompactionEvery {
		t.Errorf("full rebuild must advance the cursor over every fact, got %d ids", len(storer.consumed[0]))
	}

	// Compaction due but the facts do not fit → fold the pending fact instead.
	budgetChars := refreshFactsBudgetTokens() * stanceCharsPerToken
	bigID, _ := storer.InsertStanceDocument(context.Background(), StanceDocument{PersonID: 1, ClusterLabel: "big", Summary: "big summary", FactCount: stanceCompactionEvery - 1})
	for i := int64(1); i <= int64(stanceCompactionEvery); i++ {
		storer.facts[bigID] = append(storer.facts[bigID], factOfSize(100+i, budgetChars/4))
	}
	storer.unsummarized = storer.facts[bigID][stanceCompactionEvery-1:]
	bigMatched := storer.stances[bigID]
	if err := sm.refreshAfterVerdict(context.Background(), &bigMatched, 100+int64(stanceCompactionEvery), nil, nil); err != nil {
		t.Fatalf("refreshAfterVerdict(big): %v", err)
	}
	calls = stub.calls()
	if len(calls) != 2 || !strings.Contains(calls[1], "CURRENT SUMMARY: big summary") {
		t.Fatalf("oversized stance must fold even when compaction is due, got %d calls", len(calls))
	}
}

// Live path right after the migration: summarized_at is NULL so every fact is
// pending. A big stance must be summarized from ALL its facts (from scratch,
// chunked) rather than folding the new fact onto its stale placeholder.
func TestRefreshAfterVerdict_NeverSummarizedBigStanceFoldsFromScratch(t *testing.T) {
	stub := newRefreshLLMStub(t)
	storer := newMockStanceStorer()
	sm := newRefreshTestManager(t, stub, storer)

	budgetChars := refreshFactsBudgetTokens() * stanceCharsPerToken
	stanceID, _ := storer.InsertStanceDocument(context.Background(), StanceDocument{PersonID: 1, ClusterLabel: "stale", Summary: "stale placeholder", FactCount: 6})
	for i := int64(1); i <= 7; i++ {
		storer.facts[stanceID] = append(storer.facts[stanceID], factOfSize(i, budgetChars/3-50))
	}
	storer.unsummarized = nil // mock: every fact pending
	matched := storer.stances[stanceID]

	if err := sm.refreshAfterVerdict(context.Background(), &matched, 7, nil, nil); err != nil {
		t.Fatalf("refreshAfterVerdict: %v", err)
	}
	calls := stub.calls()
	if len(calls) != 3 {
		t.Fatalf("7 facts at 1/3 budget each must fold in 3 chunks, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "CURRENT SUMMARY: (none yet)") || strings.Contains(calls[0], "stale placeholder") {
		t.Errorf("never-summarized stance must fold from scratch, not onto the placeholder")
	}
	if !strings.Contains(calls[1], "CURRENT LABEL: label-1") || !strings.Contains(calls[2], "CURRENT SUMMARY: summary-2(fold)") {
		t.Errorf("chunks must chain label and summary")
	}
	if storer.stances[stanceID].Summary != "summary-3(fold)" || len(storer.consumed[0]) != 7 {
		t.Errorf("last chunk persisted and cursor over all 7 facts; got %+v consumed=%v", storer.stances[stanceID], storer.consumed)
	}
}

// Generic path (merge / deferred backfill / CLI): when the facts fit, a full
// rebuild (compaction on every generic refresh); when nothing is new on a
// too-big stance, a no-op; when some facts are new on a too-big stance, they
// are folded — with the tensions recorded for them — in chunks onto the
// CURRENT summary.
func TestTriggerStanceRefresh_FullFoldAndNoop(t *testing.T) {
	stub := newRefreshLLMStub(t)
	storer := newMockStanceStorer()
	sm := newRefreshTestManager(t, stub, storer)
	ctx := context.Background()

	// Small stance, already summarized, one new fact: still a full rebuild.
	smallID, _ := storer.InsertStanceDocument(ctx, StanceDocument{PersonID: 1, ClusterLabel: "l", Summary: "s", FactCount: 2})
	storer.facts[smallID] = []StanceFactInfo{factOfSize(1, 100), factOfSize(2, 100)}
	storer.unsummarized = storer.facts[smallID][1:]
	if err := sm.triggerStanceRefresh(ctx, smallID); err != nil {
		t.Fatalf("small refresh: %v", err)
	}
	if calls := stub.calls(); len(calls) != 1 || !strings.Contains(calls[0], "FACTS IN THIS STANCE") {
		t.Fatalf("small stance must be fully rebuilt in one call, got %d calls", len(calls))
	}

	// Too-big stance: each fact ~1/3 of the facts budget, 9 facts → does not fit.
	budgetChars := refreshFactsBudgetTokens() * stanceCharsPerToken
	bigID, _ := storer.InsertStanceDocument(ctx, StanceDocument{PersonID: 1, ClusterLabel: "big label", Summary: "big summary", FactCount: 9})
	var big []StanceFactInfo
	for i := int64(1); i <= 9; i++ {
		big = append(big, factOfSize(i, budgetChars/3-50))
	}
	storer.facts[bigID] = big
	if fullRefreshFits(big) {
		t.Fatal("fixture must not fit the full-rebuild budget")
	}

	// Nothing new → no LLM call.
	storer.unsummarized = []StanceFactInfo{}
	before := len(stub.calls())
	if err := sm.triggerStanceRefresh(ctx, bigID); err != nil {
		t.Fatalf("noop refresh: %v", err)
	}
	if len(stub.calls()) != before {
		t.Fatal("no new facts must mean no LLM call")
	}

	// Four new facts → two fold chunks (3 fit per chunk), chained, onto the
	// current summary, carrying the tension recorded for one of them.
	storer.unsummarized = big[5:9]
	storer.tensions = map[int64][]StanceTension{7: {{FactID: 7, OtherFactText: "an earlier position", Reason: "walked back"}}}
	if err := sm.triggerStanceRefresh(ctx, bigID); err != nil {
		t.Fatalf("fold refresh: %v", err)
	}
	calls := stub.calls()[before:]
	if len(calls) != 2 {
		t.Fatalf("expected 2 fold chunks, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "CURRENT SUMMARY: big summary") {
		t.Errorf("first chunk must fold onto the current summary")
	}
	if !strings.Contains(calls[0], "⚠ CONTRADICTS the earlier fact: « an earlier position » — walked back") {
		t.Errorf("recorded tensions must reach the generic fold")
	}
	if !strings.Contains(calls[1], "CURRENT SUMMARY: summary-2(fold)") {
		t.Errorf("second chunk must fold onto the first chunk's output")
	}
	if storer.stances[bigID].Summary != "summary-3(fold)" {
		t.Errorf("last chunk's output must be persisted, got %q", storer.stances[bigID].Summary)
	}
	if last := storer.consumed[len(storer.consumed)-1]; len(last) != 4 {
		t.Errorf("cursor must cover the 4 folded facts, got %v", last)
	}
}

// chunkNewFactsByBudget never emits an empty chunk and never exceeds the budget
// except for a single oversized fact attempted alone.
func TestChunkNewFactsByBudget(t *testing.T) {
	budgetChars := refreshFactsBudgetTokens() * stanceCharsPerToken
	facts := []refreshNewFact{
		{Text: strings.Repeat("a", budgetChars/2)},
		{Text: strings.Repeat("b", budgetChars/2)},
		{Text: strings.Repeat("c", budgetChars*2)}, // oversized: alone
		{Text: "d"},
	}
	chunks := chunkNewFactsByBudget(facts)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[1]) != 1 || !strings.HasPrefix(chunks[1][0].Text, "ccc") {
		t.Errorf("oversized fact must be attempted alone, got chunk %d facts", len(chunks[1]))
	}
	if len(chunks[2]) != 1 || chunks[2][0].Text != "d" {
		t.Errorf("trailing small fact must start a new chunk")
	}
	if got := chunkNewFactsByBudget(nil); got != nil {
		t.Errorf("no facts → no chunks")
	}
}
