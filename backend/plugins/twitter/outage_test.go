package twitter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectHandlesStopsOnUnavailableLLM: an unreachable LLM stops the
// handle loop (no further tweet fetch) and is returned; any other failure is
// recorded and the loop goes on.
func TestCollectHandlesStopsOnUnavailableLLM(t *testing.T) {
	handles := []Handle{{Handle: "a", PersonID: 1}, {Handle: "b", PersonID: 2}, {Handle: "c", PersonID: 3}, {Handle: "d", PersonID: 4}}
	targets := []int64{1, 2, 3, 4, 5}
	var called []string
	stats, err := collectHandles(context.Background(), handles, targets, func(_ context.Context, h Handle) (*Result, error) {
		called = append(called, h.Handle)
		switch h.Handle {
		case "a":
			return nil, errors.New("fetch quota")
		case "c":
			return nil, fmt.Errorf("extracting: %w", llm.ErrUnavailable)
		}
		return &Result{}, nil
	})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.ErrorContains(t, err, "fetch quota")
	assert.Equal(t, []string{"a", "b", "c"}, called, "no handle after the outage")
	assert.Equal(t, 1, stats.Targets)
	assert.Equal(t, []int64{1, 2, 5}, stats.Done, "done: the failed and the collected handles' persons, and the target without a handle")

	called = nil
	_, err = collectHandles(context.Background(), handles[:2], targets, func(_ context.Context, h Handle) (*Result, error) {
		called = append(called, h.Handle)
		return nil, errors.New("fetch quota")
	})
	require.Error(t, err)
	assert.NotErrorIs(t, err, llm.ErrUnavailable)
	assert.Equal(t, []string{"a", "b"}, called)
}

// windowFetcher returns one new tweet per fetch and records the start of
// each fetched window.
type windowFetcher struct {
	mu    sync.Mutex
	froms map[time.Time]bool
}

func (f *windowFetcher) FetchTweets(_ context.Context, _ string, opts FetchOptions) ([]Tweet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.froms[*opts.DateFrom] = true
	id := fmt.Sprint(opts.DateFrom.UnixNano())
	return []Tweet{{ID: id, Text: "Nous baisserons la TVA", URL: "https://x.com/jeanfenetre/status/" + id, CreatedAt: opts.DateFrom}}, nil
}

// TestWindowedCollectionStopsOnUnavailableLLM: a window whose extraction hits
// an unreachable LLM stops the windowed collection instead of fetching the
// next windows' tweets (API credits).
func TestWindowedCollectionStopsOnUnavailableLLM(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	store := NewStore(storer)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean fenetre", "human")
	require.NoError(t, err)
	require.NoError(t, store.AddHandle(ctx, personID, "jeanfenetre"))
	handles, err := store.GetHandlesByPersonID(ctx, personID)
	require.NoError(t, err)

	_, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	t.Cleanup(fallback.Close)
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb")
	t.Setenv("FALLBACK_CHAT_MODEL", "fm")
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	llm.MarkPrimaryDown("test")

	fetcher := &windowFetcher{froms: map[time.Time]bool{}}
	p := &Provider{Fetcher: fetcher, FactExtractor: ext, Embedder: emb, Storer: storer, Store: store}
	from, to := time.Now().AddDate(0, 0, -9), time.Now()
	_, err = p.CollectFactsWithDateWindows(ctx, handles[0], Options{DateFrom: &from, DateTo: &to, WindowDays: 3})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.Len(t, fetcher.froms, 1, "only the first window is fetched")
}

// TestWindowedCollectionExtractsLeftoversOnce: when no window brings a new
// tweet, the person's unfinished tweets are extracted once for the
// collection, not once per window (a failing tweet would use up its attempts
// in one run).
func TestWindowedCollectionExtractsLeftoversOnce(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	store := NewStore(storer)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean leftover", "human")
	require.NoError(t, err)
	require.NoError(t, store.AddHandle(ctx, personID, "jeanleftover"))
	handles, err := store.GetHandlesByPersonID(ctx, personID)
	require.NoError(t, err)
	url := "https://x.com/jeanleftover/status/1"
	_, err = storer.SaveFetchedDocuments(ctx, []core.SourceDocument{{
		SourceType: "twitter", URL: url, PersonID: personID, Content: "Bonjour à tous",
	}})
	require.NoError(t, err)

	_, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	t.Setenv("OPEN_AI_API", stubServer(t, http.StatusBadRequest, `{"error":{"message":"bad request"}}`).URL)
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)

	p := &Provider{Fetcher: emptyFetcher{}, FactExtractor: ext, Embedder: emb, Storer: storer, Store: store}
	from, to := time.Now().AddDate(0, 0, -9), time.Now()
	_, err = p.CollectFactsWithDateWindows(ctx, handles[0], Options{DateFrom: &from, DateTo: &to, WindowDays: 3})
	require.Error(t, err, "the leftover's extraction fails")
	assert.NotErrorIs(t, err, llm.ErrUnavailable)

	var attempts int
	require.NoError(t, storer.Pool().QueryRow(ctx, `SELECT attempts FROM source_documents WHERE url = $1`, url).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}
