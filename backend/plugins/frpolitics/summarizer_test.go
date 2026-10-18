package frpolitics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetaStatement: a statement that talks about the title or what could not
// be determined is a remark on the summarization, not a law statement. The
// feed of 2026-10-17 put "mais le contenu précis de l'article 7 n'est pas
// déterminable à partir du titre" into 299 ballots.
func TestMetaStatement(t *testing.T) {
	for _, s := range []string{
		"Crée un droit à l'aide à mourir, mais le contenu précis de l'article 7 n'est pas déterminable à partir du titre.",
		"Renforce la lutte contre le narcotrafic, mais le contenu précis de l'article 21 bis n'est pas déterminable à partir du titre.",
		"Modifie le code de l'énergie ; il est impossible de déterminer la direction.",
		"Le titre ne permet pas de connaître la mesure.",
	} {
		if !metaStatement(s) {
			t.Errorf("%q: want a meta statement", s)
		}
	}
	for _, s := range []string{
		"Crée un droit à l'aide à mourir.",
		"Renforce la lutte contre le narcotrafic et la criminalité organisée.",
		"Invite le Gouvernement à s'opposer à l'adoption de l'accord commercial entre l'Union européenne et le Mercosur.",
		"Détermine les conditions d'accès au titre de séjour.",
	} {
		if metaStatement(s) {
			t.Errorf("%q: want a law statement", s)
		}
	}
}

// TestSummarizeLawsSharesOneTitle: the scrutins that summarize one title (the
// articles of one bill) make one call and get one summary.
func TestSummarizeLawsSharesOneTitle(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		prompt := req.Messages[0].Content
		for _, line := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(line, "  Title:") && strings.Contains(line, "l'article") {
				t.Errorf("the summarizer must read the bill, not the article: %q", line)
			}
		}
		if n := strings.Count(prompt, "- Key:"); n != 2 {
			t.Errorf("laws in the prompt = %d, want 2 (one per title)", n)
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant",
			"content": `{"17-1": {"short_title": "Aide à mourir", "simple_statement": "Crée un droit à l'aide à mourir.", "confidence": 0.9},
			            "17-3": {"short_title": "Mayotte", "simple_statement": "Crée des mesures d'urgence pour Mayotte.", "confidence": 0.9}}`}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	t.Setenv("OPEN_AI_API", srv.URL)
	t.Setenv("OPEN_AI_KEY", "test")
	t.Setenv("OPEN_AI_CHAT_MODEL", "stub")
	t.Setenv("FALLBACK_URL", "")

	laws := []Law{
		{Key: "17-1", Title: "l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 17},
		{Key: "17-2", Title: "l'article 14 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 17},
		{Key: "17-3", Title: "l'article 16 du projet de loi d'urgence pour Mayotte (première lecture).", Legislature: 17},
	}
	got, err := SummarizeLaws(context.Background(), laws)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("LLM calls = %d, want 1", calls)
	}
	if got["17-1"].SimpleStatement != got["17-2"].SimpleStatement || got["17-1"].SimpleStatement != "Crée un droit à l'aide à mourir." {
		t.Errorf("the two articles of one bill must share the summary: %+v / %+v", got["17-1"], got["17-2"])
	}
	if got["17-3"].ShortTitle != "Mayotte" {
		t.Errorf("the other bill keeps its own summary: %+v", got["17-3"])
	}
}
