package frpolitics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"digital-mirror/backend/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// summarizerStub serves the law summarizer: every call answers the given
// confidences (by law key) or, when status is not 200, fails with it. It
// records each request's prompt.
type summarizerStub struct {
	mu      sync.Mutex
	prompts []string
	status  int
}

func (s *summarizerStub) start(t *testing.T, confidences map[string]float64) {
	t.Helper()
	reply := map[string]any{}
	for key, c := range confidences {
		reply[key] = map[string]any{"short_title": "Titre " + key, "simple_statement": "Renforce " + key, "confidence": c}
	}
	content, err := json.Marshal(reply)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": string(content)}}}})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.prompts = append(s.prompts, string(raw))
		status := s.status
		s.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"rejected"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPEN_AI_API", srv.URL)
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_CHAT_MODEL", "m")
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	t.Cleanup(llm.MarkPrimaryUp)
}

// summarized reports whether any recorded prompt mentioned the law key.
func (s *summarizerStub) summarized(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.prompts {
		if strings.Contains(p, key) {
			return true
		}
	}
	return false
}

func (s *summarizerStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = nil
}

// selectFixture is a discovered-law list whose head is unusable: a keyword
// skip, a law without ballots, a gated amendment and a low-relevance law come
// before the two usable ones.
func selectFixture() ([]Law, map[string]LawGroupStats, LawEventMap) {
	law := func(key, title string) Law {
		return Law{Key: key, Title: title, URL: "https://www.assemblee-nationale.fr/dyn/17/scrutins/" + key, Legislature: 17}
	}
	laws := []Law{
		law("17-9001", "l'ensemble du projet de loi de finances pour 2026"),
		law("17-9002", "l'ensemble de la proposition de loi visant à rien"),
		law("17-9003", "l'amendement de M. Personne visant à tout changer"),
		law("17-9004", "l'ensemble de la proposition de loi visant à baisser X"),
		law("17-9005", "l'ensemble de la proposition de loi visant à hausser Y"),
		law("17-9006", "l'ensemble de la proposition de loi visant à hausser Z"),
	}
	ballots := []VoteEvent{{AssembleeID: "PA1", DisplayName: "Jean Dupont", Position: VoteFor}}
	events := LawEventMap{"17-9001": ballots, "17-9003": ballots, "17-9004": ballots, "17-9005": ballots, "17-9006": ballots}
	return laws, map[string]LawGroupStats{}, events
}

func skippedLaws(ctx context.Context, t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows, err := store.pool.Query(ctx, `SELECT law_key, skip_reason FROM skipped_laws`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, reason string
		require.NoError(t, rows.Scan(&key, &reason))
		out[key] = reason
	}
	require.NoError(t, rows.Err())
	return out
}

// TestSelectLawsCapCountsUsableLaws: MaxLaws counts only laws that pass every
// check, so unusable laws at the head of the list do not starve the feed; the
// laws judged irrelevant are recorded and never summarized again, and the LLM
// is not asked about laws past the cap.
func TestSelectLawsCapCountsUsableLaws(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	stub := &summarizerStub{}
	stub.start(t, map[string]float64{"17-9004": 0.2, "17-9005": 0.9, "17-9006": 0.9})
	laws, groups, events := selectFixture()
	opts := Options{MaxLaws: 1}

	sel, err := selectLaws(ctx, storer, store, laws, groups, events, OrganeInfoMap{}, 0.8, opts)
	require.NoError(t, err)
	require.Len(t, sel.laws, 1)
	assert.Equal(t, "17-9005", sel.laws[0].Key)
	assert.Equal(t, 0.9, sel.summaries["17-9005"].Confidence)
	assert.Equal(t, map[string]string{"17-9001": "keyword_match", "17-9004": "low_ai_confidence"}, skippedLaws(ctx, t, store))
	assert.False(t, stub.summarized("17-9006"), "no LLM call past the cap")
	assert.False(t, stub.summarized("17-9003"), "a gated law costs no LLM call")

	// Next run: the recorded verdicts keep those laws out of the LLM.
	stub.reset()
	sel, err = selectLaws(ctx, storer, store, laws, groups, events, OrganeInfoMap{}, 0.8, opts)
	require.NoError(t, err)
	require.Len(t, sel.laws, 1)
	assert.Equal(t, "17-9005", sel.laws[0].Key)
	assert.False(t, stub.summarized("17-9004"), "a law judged irrelevant is not summarized again")
}

// TestSelectLawsSummarizerErrorFails: an LLM failure fails the selection and
// records nothing: an error is never read as low relevance.
func TestSelectLawsSummarizerErrorFails(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	stub := &summarizerStub{status: http.StatusBadRequest}
	stub.start(t, nil)
	laws, groups, events := selectFixture()

	_, err := selectLaws(ctx, storer, store, laws, groups, events, OrganeInfoMap{}, 0.8, Options{MaxLaws: 1})
	require.Error(t, err)
	assert.Equal(t, map[string]string{"17-9001": "keyword_match"}, skippedLaws(ctx, t, store))
}

// TestSummarizeLawsFailsOnLLMError: SummarizeLaws returns the LLM error
// instead of a zero-confidence summary.
func TestSummarizeLawsFailsOnLLMError(t *testing.T) {
	stub := &summarizerStub{status: http.StatusBadRequest}
	stub.start(t, nil)
	summaries, err := SummarizeLaws(context.Background(), []Law{{Key: "17-1", Title: "l'ensemble du texte"}})
	require.Error(t, err)
	assert.Nil(t, summaries)
}
