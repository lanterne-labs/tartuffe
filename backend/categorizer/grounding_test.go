package categorizer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
)

// groundingServer answers every call with reply as the assistant content.
func groundingServer(t *testing.T, reply string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", srv.URL)
	t.Setenv("OPEN_AI_CHAT_MODEL", "test-model")
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	t.Cleanup(llm.MarkPrimaryUp)
	ext, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return ext.client
}

// TestGroundFactsAppliesVerdicts checks the three verdicts: a grounded fact
// stays, a rewrite replaces the text (and clears the English), a drop
// removes the fact; a longer quote is taken only when it is a verbatim span
// of the source holding the original quote.
func TestGroundFactsAppliesVerdicts(t *testing.T) {
	source := "JOURNALISTE : Le PS veut vous museler. BRUNO RETAILLEAU : Si c'était ainsi, je partirais. Mais ce n'est pas le cas. Il a obtenu 60 %."
	facts := []backend.Fact{
		{NormalizedFactText: "Bruno Retailleau a dit que ce n'est pas le cas.", OriginalQuote: "Mais ce n'est pas le cas."},
		{NormalizedFactText: "Bruno Retailleau partirait si le PS le muselait.", NormalizedFactTextEn: "old", OriginalQuote: "Si c'était ainsi, je partirais."},
		{NormalizedFactText: "Bruno Retailleau a obtenu 60 %.", OriginalQuote: "Il a obtenu 60 %."},
		{NormalizedFactText: "Bruno Retailleau a menacé de partir.", OriginalQuote: "Si c'était ainsi, je partirais."},
	}
	reply := `{"facts":[
		{"n":1,"reasoning":"ok","verdict":"grounded"},
		{"n":2,"reasoning":"PS from the question","fact":"Bruno Retailleau a déclaré qu'il partirait dans ce cas.","verdict":"rewrite"},
		{"n":3,"reasoning":"a score","verdict":"drop"},
		{"n":4,"reasoning":"invented quote","fact":"Bruno Retailleau a menacé de partir si le PS le muselait.","quote":"Le PS veut le museler, il partirait.","verdict":"rewrite"}]}`
	client := groundingServer(t, reply)
	out, res, err := groundFacts(context.Background(), client, "Bruno Retailleau", source, "u", facts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Grounded != 1 || res.Rewritten != 2 || res.Dropped != 1 || res.Requoted != 0 || len(out) != 3 {
		t.Fatalf("outcome %+v, %d facts", res, len(out))
	}
	if out[1].NormalizedFactText != "Bruno Retailleau a déclaré qu'il partirait dans ce cas." || out[1].NormalizedFactTextEn != "" {
		t.Errorf("rewrite not applied: %+v", out[1])
	}
	if out[2].OriginalQuote != "Si c'était ainsi, je partirais." {
		t.Errorf("a quote that is not in the source must not be taken: %q", out[2].OriginalQuote)
	}
}

// TestGroundFactsRequote checks that a longer quote copied from the source
// (quote marks and spaces folded) replaces the original one.
func TestGroundFactsRequote(t *testing.T) {
	source := "Interrogé sur le RN : « Nous avons des convergences sur l’immigration,  cela ne fait pas une alliance. »"
	facts := []backend.Fact{{NormalizedFactText: "Il a des convergences avec le RN.", OriginalQuote: "cela ne fait pas une alliance"}}
	reply := `{"facts":[{"n":1,"reasoning":"referent next to it","quote":"Nous avons des convergences sur l'immigration, cela ne fait pas une alliance.","verdict":"rewrite"}]}`
	out, res, err := groundFacts(context.Background(), groundingServer(t, reply), "X", source, "u", facts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Requoted != 1 || !strings.Contains(out[0].OriginalQuote, "convergences sur l’immigration") {
		t.Fatalf("requote not applied: %+v %+v", res, out)
	}
}

// TestGroundFactsBadReplyKeepsFacts checks that a reply that does not parse
// leaves the facts unchecked rather than losing them.
func TestGroundFactsBadReplyKeepsFacts(t *testing.T) {
	facts := []backend.Fact{{NormalizedFactText: "a", OriginalQuote: "a"}, {NormalizedFactText: "b", OriginalQuote: "b"}}
	out, res, err := groundFacts(context.Background(), groundingServer(t, "not json"), "X", "a b", "u", facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || res.Unchecked != 2 {
		t.Fatalf("want both facts kept unchecked, got %d %+v", len(out), res)
	}
}

func TestQuoteContext(t *testing.T) {
	source := strings.Repeat("x ", 500) + "« Je partirais. »" + strings.Repeat(" y", 500)
	got := quoteContext(source, "\"je partirais.\"")
	if !strings.Contains(got, "« Je partirais. »") || len(got) > 2*groundingWindow+40 {
		t.Fatalf("context %d chars: %q", len(got), got)
	}
	if quoteContext("abc", "zzz") != "(not found in the source)" {
		t.Fatal("a missing quote must say so")
	}
}
