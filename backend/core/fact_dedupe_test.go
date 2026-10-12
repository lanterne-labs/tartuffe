package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/llmtest"
)

// dedupeLLMStub is an OpenAI-compatible chat endpoint that records every user
// prompt it receives and answers a fixed "different" verdict, so tests can
// assert the exact bytes the grouping gate sends.
type dedupeLLMStub struct {
	mu      sync.Mutex
	prompts []string
	server  *httptest.Server
}

func newDedupeLLMStub(t *testing.T) *dedupeLLMStub {
	t.Helper()
	s := &dedupeLLMStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.prompts = append(s.prompts, req.Messages[len(req.Messages)-1].Content)
		s.mu.Unlock()
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": `{"reasoning": "stub", "verdict": "different"}`}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *dedupeLLMStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// sharedPrefixLen returns the length of the longest common byte prefix.
func sharedPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// TestDedupeGatePromptsShareRulePrefix pins the prefix-cache layout of the
// gate prompts: two calls with different statements share every byte up to
// the first per-call datum, and the JSON response format comes after the
// statements.
func TestDedupeGatePromptsShareRulePrefix(t *testing.T) {
	stub := newDedupeLLMStub(t)
	cfg := llm.Config{BaseURL: stub.server.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	adj := &FactDedupeAdjudicator{client: cfg.NewClient("fact-dedupe-test")}
	ctx := context.Background()

	day := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	evidence := func(text string) ClaimEvidence {
		return ClaimEvidence{Text: text, Sources: []ClaimSource{{Outlet: "lemonde.fr", Quote: "une citation", PublishedAt: &day}}}
	}
	gates := map[string]struct {
		marker, lastStatement string
		call                  func(a, b string) error
	}{
		"IsSameOccurrence": {"\nStatement A: ", "Statement B:", func(a, b string) error {
			_, err := adj.IsSameOccurrence(ctx, evidence(a), evidence(b))
			return err
		}},
	}
	for name, gate := range gates {
		t.Run(name, func(t *testing.T) {
			before := len(stub.calls())
			if err := gate.call("il ira jusqu'au bout de sa candidature", "sa candidature irait jusqu'au bout"); err != nil {
				t.Fatalf("first call: %v", err)
			}
			if err := gate.call("a annoncé le retrait du plaider-coupable", "présentera son projet de loi le 29 juin"); err != nil {
				t.Fatalf("second call: %v", err)
			}
			calls := stub.calls()[before:]
			if len(calls) != 2 {
				t.Fatalf("expected 2 LLM calls, got %d", len(calls))
			}
			p1, p2 := calls[0], calls[1]

			marker := strings.Index(p1, gate.marker)
			if marker < 0 {
				t.Fatalf("no %q data marker in prompt:\n%s", gate.marker, p1)
			}
			if shared := sharedPrefixLen(p1, p2); shared < marker {
				t.Errorf("shared prefix is %d bytes, diverges before the data marker at %d:\n%s", shared, marker, p1[shared:min(len(p1), shared+80)])
			}
			if marker < 1000 {
				t.Errorf("cacheable rule prefix is only %d bytes; the rulebook should precede the data", marker)
			}
			format := strings.Index(p1, "Return ONLY strict JSON")
			if stmtB := strings.Index(p1, gate.lastStatement); format < 0 || format < stmtB {
				t.Errorf("response format must come after the statements (format at %d, last statement at %d)", format, stmtB)
			}
		})
	}
}

// The occurrence gate shows each fact with its outlet, date and quote, and
// asks for the verdict as the last key of the reply.
func TestOccurrencePromptCarriesEvidence(t *testing.T) {
	day := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	prompt := buildOccurrencePrompt(
		ClaimEvidence{Text: "Il a signé un accord.", Sources: []ClaimSource{{Outlet: "bfmtv.com", Quote: "Ils ont conclu\nune alliance.", PublishedAt: &day}}},
		ClaimEvidence{Text: "Il a paraphé un protocole.", Sources: []ClaimSource{{Outlet: "lemonde.fr", Quote: "un protocole d'accord"}}},
	)
	for _, want := range []string{
		"Statement A: Il a signé un accord.\n  source: bfmtv.com, published 2026-09-04, quote: \"Ils ont conclu une alliance.\"",
		"Statement B: Il a paraphé un protocole.\n  source: lemonde.fr, published unknown date, quote: \"un protocole d'accord\"",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt[len(occurrenceRules):])
		}
	}
	if !strings.HasSuffix(prompt, `"verdict": "same" | "different" | "bundle"}`) {
		t.Errorf("the verdict must be the last key of the reply format")
	}
}

// The verdict needs both orders: "same" twice is same, a "bundle" answer in
// either order is contained, one "different" is different.
func TestIsSameOccurrenceNeedsBothOrders(t *testing.T) {
	for _, tc := range []struct {
		replies []string
		want    OccurrenceVerdict
		calls   int
	}{
		{[]string{"same", "same"}, OccurrenceSame, 2},
		{[]string{"same", "bundle"}, OccurrenceContained, 2},
		{[]string{"bundle", "same"}, OccurrenceContained, 2},
		{[]string{"same", "different"}, OccurrenceDifferent, 2},
		{[]string{"different"}, OccurrenceDifferent, 1},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content := `{"occasion_a": "a", "occasion_b": "b", "reasoning": "r", "verdict": "` + tc.replies[calls] + `"}`
			calls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
			})
		}))
		cfg := llm.Config{BaseURL: server.URL + "/v1", APIKey: "test", ChatModel: "stub"}
		adj := &FactDedupeAdjudicator{client: cfg.NewClient("fact-dedupe-test")}
		got, err := adj.IsSameOccurrence(context.Background(), ClaimEvidence{Text: "A"}, ClaimEvidence{Text: "B"})
		server.Close()
		if err != nil || got != tc.want || calls != tc.calls {
			t.Errorf("replies %v: got verdict %v, %d calls, err %v; want %v, %d calls", tc.replies, got, calls, err, tc.want, tc.calls)
		}
	}
}

// TestOccurrenceGateGolden is the local-only golden set of the grouping gate:
// real prod pairs with their quotes, dates and outlets (testdata/
// occurrence_cases.json), labelled one by one with that evidence. Every pair
// meets the evidence rule, as every pair the gate reads in production does.
// "same" and "bundle" cases must be groupable, "different" cases must not.
// A case with a "gap" is a known miss: a mismatch is logged, not failed.
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set, so it never runs in CI.
//
//	source .env; go test -v -run TestOccurrenceGateGolden ./backend/core/
func TestOccurrenceGateGolden(t *testing.T) {
	llmtest.Setup(t)
	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	type source struct {
		Outlet string `json:"outlet"`
		Date   string `json:"date"`
		Quote  string `json:"quote"`
	}
	var cases []struct {
		Name     string   `json:"name"`
		Want     string   `json:"want"`
		Gap      string   `json:"gap"`
		FactA    string   `json:"fact_a"`
		SourcesA []source `json:"sources_a"`
		FactB    string   `json:"fact_b"`
		SourcesB []source `json:"sources_b"`
	}
	raw, err := os.ReadFile("testdata/occurrence_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	// evidence takes the t of the case: a case runs on its own goroutine.
	evidence := func(t *testing.T, text string, sources []source) ClaimEvidence {
		t.Helper()
		e := ClaimEvidence{Text: text}
		for _, s := range sources {
			cs := ClaimSource{Outlet: s.Outlet, Quote: s.Quote}
			if s.Date != "" {
				d, err := time.Parse("2006-01-02", s.Date)
				if err != nil {
					t.Fatalf("bad date %q: %v", s.Date, err)
				}
				cs.PublishedAt = &d
			}
			e.Sources = append(e.Sources, cs)
		}
		return e
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()
	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed, gaps int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				got, err := adj.IsSameOccurrence(ctx, evidence(t, tc.FactA, tc.SourcesA), evidence(t, tc.FactB, tc.SourcesB))
				if err != nil {
					t.Fatalf("gate error: %v", err)
				}
				want := tc.Want != "different"
				mu.Lock()
				defer mu.Unlock()
				switch {
				case got.Groupable() == want:
					passed++
				case tc.Gap != "":
					gaps++
					t.Logf("known gap (groupable=%v, label %s): %s", got.Groupable(), tc.Want, tc.Gap)
				default:
					failed++
					t.Errorf("groupable=%v, label %s\n    A: %s\n    B: %s", got.Groupable(), tc.Want, tc.FactA, tc.FactB)
				}
			})
		}
	})
	t.Logf("=== OCCURRENCE GOLDEN: %d passed, %d failed, %d known gaps (of %d) ===", passed, failed, gaps, len(cases))
}
