package twitter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"digital-mirror/backend/core"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWebSearcher struct{ results []search.SearchResult }

func (f fakeWebSearcher) SearchWeb(string, int) ([]search.SearchResult, error) {
	return f.results, nil
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

// TestDiscoverUnavailableLLMLeavesPersonUnsearched: when the LLM cannot be
// reached, discovery fails with llm.ErrUnavailable and the person does not
// enter the 7-day cooldown, so the next run searches them again. Once the LLM
// answers, the person is marked searched.
func TestDiscoverUnavailableLLMLeavesPersonUnsearched(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean discovery", "human")
	require.NoError(t, err)

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"HANDLE: NONE"}}]}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	defer fallback.Close()
	t.Setenv("OPEN_AI_API", primary.URL)
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_CHAT_MODEL", "m")
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_CHAT_MODEL", "fb")
	t.Setenv("FALLBACK_KEY", "k")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	defer llm.MarkPrimaryUp()

	dc := &discoverer{
		store:      store,
		searcher:   fakeWebSearcher{results: []search.SearchResult{{URL: "https://x.com/jeandisco", Title: "Jean (@jeandisco)"}}},
		client:     llm.ConfigFromEnv().NewClient("twitter-discovery-test"),
		httpClient: &http.Client{Transport: failingTransport{}},
	}
	person := core.PersonRecord{ID: personID, Name: "jean discovery"}

	llm.MarkPrimaryDown("test") // primary skipped, fallback out of credits
	_, err = dc.discover(ctx, person, DiscoveryOptions{})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	searchedAt, err := store.GetSearchedAt(ctx, personID)
	require.NoError(t, err)
	assert.Nil(t, searchedAt, "an LLM outage must not put the person in the cooldown")

	llm.MarkPrimaryUp()
	confirmed, err := dc.discover(ctx, person, DiscoveryOptions{})
	require.NoError(t, err)
	assert.False(t, confirmed)
	searchedAt, err = store.GetSearchedAt(ctx, personID)
	require.NoError(t, err)
	assert.NotNil(t, searchedAt, "an answered LLM call concludes the search")
}

type failingWebSearcher struct{}

func (failingWebSearcher) SearchWeb(string, int) ([]search.SearchResult, error) {
	return nil, errors.New("search quota exhausted")
}

// TestDiscoverOtherFailuresEnterCooldown: a failed web search or an LLM call
// rejected for another reason than an outage puts the person in the 7-day
// cooldown, so a failure that would repeat does not re-spend a web search
// every run.
func TestDiscoverOtherFailuresEnterCooldown(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"prompt too long"}}`)
	}))
	defer rejecting.Close()
	t.Setenv("OPEN_AI_API", rejecting.URL)
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_CHAT_MODEL", "m")
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	defer llm.MarkPrimaryUp()

	for name, searcher := range map[string]search.WebSearcher{
		"search failed": failingWebSearcher{},
		"llm rejected":  fakeWebSearcher{results: []search.SearchResult{{URL: "https://x.com/jeandisco", Title: "Jean (@jeandisco)"}}},
	} {
		t.Run(name, func(t *testing.T) {
			personID, err := storer.FindOrInsertPersonWithType(ctx, "jean "+name, "human")
			require.NoError(t, err)
			dc := &discoverer{
				store:      store,
				searcher:   searcher,
				client:     llm.ConfigFromEnv().NewClient("twitter-discovery-test"),
				httpClient: &http.Client{Transport: failingTransport{}},
			}
			confirmed, err := dc.discover(ctx, core.PersonRecord{ID: personID, Name: "jean " + name}, DiscoveryOptions{})
			require.NoError(t, err)
			assert.False(t, confirmed)
			searchedAt, err := store.GetSearchedAt(ctx, personID)
			require.NoError(t, err)
			assert.NotNil(t, searchedAt)
		})
	}
}
