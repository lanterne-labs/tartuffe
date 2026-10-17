package news

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIngestTargetsStopsOnUnavailableLLM: an unreachable LLM stops the target
// loop and is returned with the failures collected before it; any other
// failure is recorded and the loop goes on.
func TestIngestTargetsStopsOnUnavailableLLM(t *testing.T) {
	targets := []plugins.Target{{Name: "A", PersonID: 1}, {Name: "B", PersonID: 2}, {Name: "C", PersonID: 3}, {Name: "D", PersonID: 4}}
	var called []string
	stats, err := ingestTargets(context.Background(), targets, func(_ context.Context, tg plugins.Target) (*Result, error) {
		called = append(called, tg.Name)
		switch tg.Name {
		case "A":
			return nil, errors.New("search quota")
		case "C":
			return nil, fmt.Errorf("extracting: %w", llm.ErrUnavailable)
		}
		return &Result{FactsExtracted: 1}, nil
	})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.NotContains(t, err.Error(), "search quota", "other failures are in the details, not the error")
	assert.Equal(t, []string{"A", "B", "C"}, called, "no target after the outage")
	assert.Equal(t, 1, stats.Targets)
	assert.Equal(t, []string{"A: search quota"}, stats.Details)
	assert.Equal(t, []int64{1, 2}, stats.Done, "the failed and the collected targets are done, not the interrupted one")
	assert.Equal(t, []int64{3}, stats.Fetched, "the interrupted target was fetched; only its extraction is left")

	called = nil
	_, err = ingestTargets(context.Background(), targets[:2], func(_ context.Context, tg plugins.Target) (*Result, error) {
		called = append(called, tg.Name)
		return nil, errors.New("search quota")
	})
	require.NoError(t, err, "other failures are only recorded")
	assert.Equal(t, []string{"A", "B"}, called)
}

// TestIngestTargetsStopsAtDeadline: an expired context starts no target; a
// target cut by the cancellation is neither done nor a failure, and no
// target starts after it.
func TestIngestTargetsStopsAtDeadline(t *testing.T) {
	targets := []plugins.Target{{Name: "A", PersonID: 1}, {Name: "B", PersonID: 2}, {Name: "C", PersonID: 3}}
	var called []string
	collect := func(cut func()) func(context.Context, plugins.Target) (*Result, error) {
		return func(ctx context.Context, tg plugins.Target) (*Result, error) {
			called = append(called, tg.Name)
			if tg.Name == "B" {
				cut()
				return nil, fmt.Errorf("scraping: %w", ctx.Err())
			}
			return &Result{FactsExtracted: 1}, nil
		}
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	stats, err := ingestTargets(expired, targets, collect(func() {}))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, called, "no target starts after the deadline")
	assert.Empty(t, stats.Done)

	ctx, cut := context.WithCancel(context.Background())
	defer cut()
	stats, err = ingestTargets(ctx, targets, collect(cut))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"A", "B"}, called, "no target after the cut")
	assert.Equal(t, []int64{1}, stats.Done, "the cut target is not done")
	assert.Empty(t, stats.Details, "the cut target is not a failure")
}

// windowSearcher returns one new article per search and records the start
// of each searched window.
type windowSearcher struct {
	mu    sync.Mutex
	froms map[time.Time]bool
}

func (s *windowSearcher) SearchNews(_ string, opts search.NewsSearchOptions) ([]search.NewsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.froms[opts.DateFrom] = true
	link := fmt.Sprintf("https://example.test/%d-%s", opts.DateFrom.UnixNano(), opts.Language)
	return []search.NewsResult{{Link: link, Title: "Article"}}, nil
}

// longPage is a scraper returning enough content to be extracted.
type longPage struct{}

func (longPage) ExtractContent(url string) (*extractor.ExtractedContent, error) {
	return &extractor.ExtractedContent{URL: url, Content: strings.Repeat("Jean Dupont a parlé de la TVA. ", 20)}, nil
}

// chatDown makes every chat call fail with llm.ErrUnavailable: the primary is
// marked down (breaker open) and the fallback has no credits.
func chatDown(t *testing.T) {
	t.Helper()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	t.Cleanup(fallback.Close)
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb")
	t.Setenv("FALLBACK_CHAT_MODEL", "fm")
	llm.MarkPrimaryDown("test")
}

// TestWindowedCollectionStopsOnUnavailableLLM: a window whose extraction hits
// an unreachable LLM stops the date-windowed collection instead of searching
// and scraping the next windows.
func TestWindowedCollectionStopsOnUnavailableLLM(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean fenetre", "human")
	require.NoError(t, err)

	stubLLM(t, noFactReply, http.StatusOK, `{}`)
	chatDown(t)
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	searcher := &windowSearcher{froms: map[time.Time]bool{}}
	p := NewProvider(searcher, longPage{}, ext, nil, storer, &core.StanceManager{})

	from, to := time.Now().AddDate(0, 0, -9), time.Now()
	_, err = p.CollectFactsWithDateWindows(ctx, plugins.Target{Name: "jean fenetre", PersonID: personID},
		Options{DateFrom: &from, DateTo: &to, WindowDays: 3, MaxParse: 5})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.Len(t, searcher.froms, 1, "only the first window is searched")
}

// TestWindowedCollectionExtractsLeftoversOnce: when no window brings anything
// new, the target's unfinished documents are extracted once for the
// collection, not once per window (a failing document would use up its
// attempts in one run).
func TestWindowedCollectionExtractsLeftoversOnce(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean leftover", "human")
	require.NoError(t, err)
	url := "https://example.test/leftover"
	_, err = storer.SaveFetchedDocuments(ctx, []core.SourceDocument{{
		SourceType: "news", URL: url, PersonID: personID, Content: "Un article.",
	}})
	require.NoError(t, err)

	_, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	t.Setenv("OPEN_AI_API", stubServer(t, http.StatusBadRequest, `{"error":{"message":"bad request"}}`).URL)
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	p := NewProvider(emptyNewsSearcher{}, nil, ext, emb, storer, &core.StanceManager{})

	from, to := time.Now().AddDate(0, 0, -9), time.Now()
	_, err = p.CollectFactsWithDateWindows(ctx, plugins.Target{Name: "jean leftover", PersonID: personID},
		Options{DateFrom: &from, DateTo: &to, WindowDays: 3, MaxParse: 5})
	require.Error(t, err, "the leftover's extraction fails")
	assert.NotErrorIs(t, err, llm.ErrUnavailable)

	var attempts int
	require.NoError(t, storer.Pool().QueryRow(ctx, `SELECT attempts FROM source_documents WHERE url = $1`, url).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}

// countingSearcher counts searches and returns nothing.
type countingSearcher struct{ calls int }

func (s *countingSearcher) SearchNews(string, search.NewsSearchOptions) ([]search.NewsResult, error) {
	s.calls++
	return nil, nil
}

// TestResumeCollectExtractsWithoutSearching: a target fetched before an LLM
// outage (IngestOptions.ExtractOnly) only has its stored documents extracted
// on the retry: no search (SERP credits), no scrape. Another target is
// searched as usual.
func TestResumeCollectExtractsWithoutSearching(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean reprise", "human")
	require.NoError(t, err)
	url := "https://example.test/reprise"
	_, err = storer.SaveFetchedDocuments(ctx, []core.SourceDocument{{
		SourceType: "news", URL: url, PersonID: personID, Content: "Un article.",
	}})
	require.NoError(t, err)

	ext, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	searcher := &countingSearcher{}
	p := NewProvider(searcher, nil, ext, emb, storer, &core.StanceManager{})
	collect := p.resumeCollect([]int64{personID}, Options{MaxParse: 5})

	_, err = collect(ctx, plugins.Target{Name: "jean reprise", PersonID: personID})
	require.NoError(t, err)
	assert.Zero(t, searcher.calls, "a fetched target is not searched again")
	var state string
	require.NoError(t, storer.Pool().QueryRow(ctx, `SELECT state FROM source_documents WHERE url = $1`, url).Scan(&state))
	assert.NotEqual(t, "fetched", state, "the stored document is extracted")

	_, err = collect(ctx, plugins.Target{Name: "autre", PersonID: personID + 1000})
	require.NoError(t, err)
	assert.Equal(t, 2, searcher.calls, "any other target is searched (one call per language)")
}

// failingSearcher fails every search, as a provider out of quota does.
type failingSearcher struct{ calls int }

func (s *failingSearcher) SearchNews(string, search.NewsSearchOptions) ([]search.NewsResult, error) {
	s.calls++
	return nil, errors.New("search quota")
}

// TestWindowedCollectionDoesNotLogAFailedSearch: a window whose search failed
// in every language is not logged as searched, so a later run searches it.
// Before, the failure counted as "no results" and the window was never
// searched again.
func TestWindowedCollectionDoesNotLogAFailedSearch(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean quota", "human")
	require.NoError(t, err)
	_, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	searcher := &failingSearcher{}
	p := NewProvider(searcher, longPage{}, ext, emb, storer, &core.StanceManager{})

	from, to := time.Now().AddDate(0, 0, -6), time.Now()
	target := plugins.Target{Name: "jean quota", PersonID: personID}
	res, err := p.CollectFactsWithDateWindows(ctx, target, Options{DateFrom: &from, DateTo: &to, WindowDays: 3, MaxParse: 5})
	require.NoError(t, err, "a failed window is recorded and the collection goes on")
	assert.Len(t, res.Errors, 2, "one failure per window")
	assert.Equal(t, 4, searcher.calls, "2 windows, 2 languages each")
	covered, err := NewStore(storer).NewsWindowsCover(ctx, []int64{personID}, from, to)
	require.NoError(t, err)
	assert.False(t, covered, "a failed window is not logged as searched")
}
