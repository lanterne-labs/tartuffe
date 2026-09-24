package categorizer

import (
	"context"
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
