package search

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newSerperWithMock returns a SerperProvider whose baseURL points at the given
// test server, plus a teardown function.
func newSerperWithMock(t *testing.T, handler http.HandlerFunc) (*SerperProvider, func()) {
	t.Helper()
	ts := httptest.NewServer(handler)
	p := &SerperProvider{
		apiKey:     "test-key",
		baseURL:    ts.URL,
		httpClient: ts.Client(),
	}
	return p, ts.Close
}

func TestSerper_BuildTBS(t *testing.T) {
	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want string
	}{
		{"both zero", time.Time{}, time.Time{}, ""},
		{
			"both set",
			time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
			"cdr:1,cd_min:3/1/2026,cd_max:5/15/2026",
		},
		{
			"single digit month and day",
			time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
			"cdr:1,cd_min:1/5/2026,cd_max:9/9/2026",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildSerperTBS(c.from, c.to)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSerper_SearchNews_RequestShape(t *testing.T) {
	var capturedBody serperRequestBody
	var capturedHeader http.Header
	var capturedPath string

	p, done := newSerperWithMock(t, func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		capturedPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"news":[{"title":"t","link":"https://x.test/1","snippet":"s","source":"X","date":"2026-05-10T08:00:00Z"}]}`))
	})
	defer done()

	results, err := p.SearchNews("Emmanuel Macron", NewsSearchOptions{
		DateFrom: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		DateTo:   time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
		Limit:    5,
		Location: "France",
	})
	if err != nil {
		t.Fatalf("SearchNews error: %v", err)
	}

	if capturedPath != "/news" {
		t.Errorf("path: got %q want /news", capturedPath)
	}
	if got := capturedHeader.Get("X-API-KEY"); got != "test-key" {
		t.Errorf("X-API-KEY: got %q want test-key", got)
	}
	if got := capturedHeader.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q want application/json", got)
	}
	if capturedBody.Query != "Emmanuel Macron" {
		t.Errorf("q: got %q", capturedBody.Query)
	}
	if capturedBody.Num != 5 {
		t.Errorf("num: got %d want 5", capturedBody.Num)
	}
	if capturedBody.GL != "fr" {
		t.Errorf("gl: got %q want fr", capturedBody.GL)
	}
	if capturedBody.TBS != "cdr:1,cd_min:5/1/2026,cd_max:5/15/2026" {
		t.Errorf("tbs: got %q", capturedBody.TBS)
	}

	if len(results) != 1 {
		t.Fatalf("results: got %d want 1", len(results))
	}
	if results[0].Link != "https://x.test/1" {
		t.Errorf("Link: got %q", results[0].Link)
	}
	if results[0].Source != "X" {
		t.Errorf("Source: got %q", results[0].Source)
	}
	if results[0].Date == nil {
		t.Errorf("Date should be parsed from ISO timestamp, got nil")
	}
}

func TestSerper_SearchNews_Pagination(t *testing.T) {
	// With Limit = serperMaxPerPage + 5, the provider should issue two calls:
	//   page=1 num=serperMaxPerPage   (server returns exactly num items)
	//   page=2 num=5                  (server returns 5 items)
	type call struct {
		page int
		num  int
	}
	var calls []call

	p, done := newSerperWithMock(t, func(w http.ResponseWriter, r *http.Request) {
		var body serperRequestBody
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, call{page: body.Page, num: body.Num})

		// Return exactly body.Num items so the loop only stops when limit is reached.
		items := make([]map[string]any, 0, body.Num)
		for i := 0; i < body.Num; i++ {
			items = append(items, map[string]any{
				"title": "t",
				"link":  "https://x.test/p?page=" + string(rune('0'+body.Page)) + "&i=" + string(rune('a'+i%26)),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"news": items})
	})
	defer done()

	results, err := p.SearchNews("q", NewsSearchOptions{Limit: serperMaxPerPage + 5})
	if err != nil {
		t.Fatalf("SearchNews error: %v", err)
	}
	if len(results) != serperMaxPerPage+5 {
		t.Errorf("results: got %d want %d", len(results), serperMaxPerPage+5)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 paginated calls, got %d", len(calls))
	}
	if calls[0].page != 1 || calls[0].num != serperMaxPerPage {
		t.Errorf("call 0: got page=%d num=%d, want page=1 num=%d", calls[0].page, calls[0].num, serperMaxPerPage)
	}
	if calls[1].page != 2 || calls[1].num != 5 {
		t.Errorf("call 1: got page=%d num=%d, want page=2 num=5", calls[1].page, calls[1].num)
	}
}

func TestSerper_SearchWeb(t *testing.T) {
	p, done := newSerperWithMock(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path: got %q want /search", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organic":[{"title":"Macron","link":"https://elysee.fr","snippet":"president"}]}`))
	})
	defer done()

	results, err := p.SearchWeb("Emmanuel Macron", 10)
	if err != nil {
		t.Fatalf("SearchWeb error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results: got %d want 1", len(results))
	}
	if results[0].URL != "https://elysee.fr" {
		t.Errorf("URL: got %q", results[0].URL)
	}
}

func TestSerper_SearchImages(t *testing.T) {
	p, done := newSerperWithMock(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images" {
			t.Errorf("path: got %q want /images", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[{"title":"x","imageUrl":"https://i.test/full.jpg","thumbnailUrl":"https://i.test/t.jpg","imageWidth":1200,"imageHeight":800,"source":"site.test","position":1}]}`))
	})
	defer done()

	results, err := p.SearchImages("q", ImageSearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("SearchImages error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results: got %d want 1", len(results))
	}
	if results[0].Original != "https://i.test/full.jpg" || results[0].Thumbnail != "https://i.test/t.jpg" {
		t.Errorf("image URLs not parsed: %+v", results[0])
	}
	if results[0].OriginalWidth != 1200 || results[0].OriginalHeight != 800 {
		t.Errorf("image dimensions: got %dx%d", results[0].OriginalWidth, results[0].OriginalHeight)
	}
}

func TestSerper_ErrorBody(t *testing.T) {
	p, done := newSerperWithMock(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	})
	defer done()

	_, err := p.SearchNews("q", NewsSearchOptions{})
	if err == nil {
		t.Fatal("expected error from non-200 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention status 403: %v", err)
	}
}

func TestSerper_Interfaces(t *testing.T) {
	p := &SerperProvider{apiKey: "k", baseURL: serperDefaultBaseURL, httpClient: http.DefaultClient}
	var _ NewsSearcher = p
	var _ WebSearcher = p
	var _ ImageSearcher = p
	var _ SearchProvider = p
}

func TestSerper_Integration(t *testing.T) {
	loadEnv(t)
	apiKey := os.Getenv("SERPER_API_KEY")
	if apiKey == "" {
		t.Skip("SERPER_API_KEY not set")
	}

	p, err := NewSerperProvider(apiKey)
	if err != nil {
		t.Fatalf("NewSerperProvider: %v", err)
	}

	results, err := p.SearchNews("Emmanuel Macron", NewsSearchOptions{
		Location: "France",
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("SearchNews: %v", err)
	}
	t.Logf("got %d results", len(results))
	for i, r := range results {
		t.Logf("  %d: %s — %s", i+1, r.Title, r.Link)
	}
}
