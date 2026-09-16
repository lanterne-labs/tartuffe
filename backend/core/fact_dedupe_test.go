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

	"github.com/joho/godotenv"
)

// dedupeLLMStub is an OpenAI-compatible chat endpoint that records every user
// prompt it receives and answers a fixed same=false verdict, so tests can
// assert the exact bytes the dedupe gates send.
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
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": `{"same": false, "reasoning": "stub"}`}}},
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

// TestDedupeGatePromptsShareRulePrefix pins the prefix-cache layout of both
// gate prompts: two calls with different subjects and statements share every
// byte up to the first per-call datum ("Person:"), the subject never appears
// before it, and the JSON response format comes after the statements.
func TestDedupeGatePromptsShareRulePrefix(t *testing.T) {
	stub := newDedupeLLMStub(t)
	cfg := llm.Config{BaseURL: stub.server.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	adj := &FactDedupeAdjudicator{client: cfg.NewClient("fact-dedupe-test")}
	ctx := context.Background()

	gates := map[string]func(ctx context.Context, subject, a, b string) (bool, error){
		"IsSameClaim":    adj.IsSameClaim,
		"IsSamePosition": adj.IsSamePosition,
	}
	for name, gate := range gates {
		t.Run(name, func(t *testing.T) {
			before := len(stub.calls())
			if _, err := gate(ctx, "Bruno Retailleau", "il ira jusqu'au bout de sa candidature", "sa candidature irait jusqu'au bout"); err != nil {
				t.Fatalf("first call: %v", err)
			}
			if _, err := gate(ctx, "Gérald Darmanin", "a annoncé le retrait du plaider-coupable", "présentera son projet de loi le 29 juin"); err != nil {
				t.Fatalf("second call: %v", err)
			}
			calls := stub.calls()[before:]
			if len(calls) != 2 {
				t.Fatalf("expected 2 LLM calls, got %d", len(calls))
			}
			p1, p2 := calls[0], calls[1]

			marker := strings.Index(p1, "\nPerson: ")
			if marker < 0 {
				t.Fatalf("no Person: data marker in prompt:\n%s", p1)
			}
			if shared := sharedPrefixLen(p1, p2); shared < marker {
				t.Errorf("shared prefix is %d bytes, diverges before the data marker at %d:\n%s", shared, marker, p1[shared:min(len(p1), shared+80)])
			}
			if marker < 1000 {
				t.Errorf("cacheable rule prefix is only %d bytes; the rulebook should precede the data", marker)
			}
			if strings.Contains(p1[:marker], "Bruno Retailleau") {
				t.Errorf("subject appears before the data marker")
			}
			format := strings.Index(p1, "Return ONLY strict JSON")
			if stmtB := strings.Index(p1, "STATEMENT B:"); format < 0 || format < stmtB {
				t.Errorf("response format must come after the statements (format at %d, STATEMENT B at %d)", format, stmtB)
			}
		})
	}
}

// TestFactDedupeAdjudicatorGolden is the local-only golden set for the
// gray-zone fact-dedup adjudicator. It asserts genuine restatements are merged
// (same=true) and that opposing stances, intention-vs-action pairs, and
// different-specifics pairs are kept apart (same=false).
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set, so it never runs in CI. The
// endpoint is the local GPU (OPEN_AI_API), so each run is free.
//
//	source .env; go test -v -run TestFactDedupeAdjudicatorGolden ./backend/core/
func TestFactDedupeAdjudicatorGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env")

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping fact-dedupe golden test: OPEN_AI_KEY not set (local-only).")
	}

	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	cases := []struct {
		name    string
		subject string
		factA   string
		factB   string
		want    bool // true = same claim (dedup)
	}{
		{
			// The real prod pair (facts 4740 / 4897) — cosine 0.78, verbs
			// PROMISED vs CLAIMED — that produced duplicate contradiction #146.
			name:    "retailleau_candidacy_restatement",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau a déclaré qu'il ne renoncera pas à sa candidature et qu'il ira jusqu'au bout de sa candidature.",
			factB:   "Bruno Retailleau a affirmé que sa candidature irait jusqu'au bout.",
			want:    true,
		},
		{
			name:    "opposing_stance_on_primary",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau plaide pour une grande primaire de la droite.",
			factB:   "Bruno Retailleau a refusé l'idée d'une primaire.",
			want:    false,
		},
		{
			name:    "intention_vs_contrary_action",
			subject: "Le Rassemblement national",
			factA:   "Le Rassemblement national a promis de maintenir l'âge de départ à la retraite à 62 ans.",
			factB:   "Le Rassemblement national a renoncé à abroger la réforme portant l'âge de la retraite à 64 ans.",
			want:    false,
		},
		{
			name:    "different_specifics",
			subject: "Gérald Darmanin",
			factA:   "Gérald Darmanin a annoncé le retrait du plaider-coupable criminel pour les crimes de sang.",
			factB:   "Gérald Darmanin présentera son projet de loi le 29 juin devant l'Assemblée nationale.",
			want:    false,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := adj.IsSameClaim(ctx, tc.subject, tc.factA, tc.factB)
			if err != nil {
				t.Fatalf("IsSameClaim error: %v", err)
			}
			if got != tc.want {
				failed++
				t.Errorf("got same=%v want same=%v\n    A: %s\n    B: %s", got, tc.want, tc.factA, tc.factB)
				return
			}
			passed++
			t.Logf("same=%v ✓", got)
		})
	}
	t.Logf("=== FACT-DEDUPE GOLDEN: %d/%d passed (%d failed) ===", passed, passed+failed, failed)
}
