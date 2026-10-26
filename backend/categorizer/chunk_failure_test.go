package categorizer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
)

// TestExtractFactsWithConfig_FailedChunkFailsDocument: when one chunk of a
// chunked document fails, the whole extraction fails. Returning the other
// chunks' facts with a nil error would get the document marked extracted and
// its content deleted, losing the failed chunk for good.
func TestExtractFactsWithConfig_FailedChunkFailsDocument(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"[]"}}]}`)
			return
		}
		// No content: the chat client fails this call without retrying.
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", srv.URL)
	t.Setenv("OPEN_AI_CHAT_MODEL", "test-model")
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	defer llm.MarkPrimaryUp()

	ext, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("Le député a déclaré soutenir la réforme des retraites. ", 2000)
	facts, err := ext.ExtractFactsWithConfig(context.Background(), promptrules.FactExtractorConfig{
		TargetName: "Jean Dupont",
		SourceURL:  "https://example.test/article",
		Content:    content,
	})
	if err == nil {
		t.Fatalf("want an error when a chunk fails, got %d facts and nil", len(facts))
	}
	if calls.Load() != 2 {
		t.Fatalf("want extraction to stop at the failed chunk (2 calls), got %d", calls.Load())
	}
}

// TestExtraction_RetriesAReplyThatDoesNotParse: one malformed JSON reply is
// asked again (run 112: it failed every tweet of a handle's batch); a second
// one fails the call.
func TestExtraction_RetriesAReplyThatDoesNotParse(t *testing.T) {
	const fact = `[{"subject_entity":"Jean Dupont","subject_entity_type":"human","normalized_fact":"Jean Dupont a déclaré soutenir la réforme.","lang":"fr","canonical_verb":"SUPPORTED","embedding_text":"réforme","original_quote":"soutenir la réforme"}]`
	for _, tc := range []struct {
		name      string
		replies   []string
		wantFacts int
		wantErr   bool
	}{
		{"recovered on the retry", []string{`[{"subject_entity": _broken`, fact}, 1, false},
		{"two bad replies fail the call", []string{`[{"subject_entity": _broken`, `not json`}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				n := int(calls.Add(1)) - 1
				w.Header().Set("Content-Type", "application/json")
				body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": tc.replies[n]}}}})
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			t.Setenv("OPEN_AI_KEY", "k")
			t.Setenv("OPEN_AI_API", srv.URL)
			t.Setenv("OPEN_AI_CHAT_MODEL", "test-model")
			t.Setenv("FALLBACK_URL", "")
			t.Setenv("CF_ACCESS_CLIENT_ID", "")
			defer llm.MarkPrimaryUp()

			ext, err := NewOpenAIExtractorFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			facts, err := ext.ExtractFactsWithConfig(context.Background(), promptrules.FactExtractorConfig{
				TargetName: "Jean Dupont", SourceURL: "https://example.test/a", Content: "Jean Dupont a déclaré soutenir la réforme.",
			})
			if (err != nil) != tc.wantErr || len(facts) != tc.wantFacts || calls.Load() != 2 {
				t.Fatalf("err=%v facts=%d calls=%d; want err=%v facts=%d calls=2", err, len(facts), calls.Load(), tc.wantErr, tc.wantFacts)
			}
		})
	}
}

// A chunk takes the room the endpoint's context leaves: on Claude the static
// chunk size binds, on the local box the prompt leaves little room.
func TestDynamicChunkBudgetFollowsTheContext(t *testing.T) {
	const prompt = 30000 // chars of the rendered prompt without content
	local := dynamicChunkBudgetChars(prompt, llm.LocalContextTokens)
	claude := dynamicChunkBudgetChars(prompt, 200000)
	if local >= chunkSizeChars {
		t.Errorf("local budget %d, want under the static chunk %d", local, chunkSizeChars)
	}
	if claude < chunkSizeChars {
		t.Errorf("claude budget %d, want at least the static chunk %d", claude, chunkSizeChars)
	}
}
