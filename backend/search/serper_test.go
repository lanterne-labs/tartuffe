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
}

func TestSerper_Integration(t *testing.T) {
	loadEnv(t)
	apiKey := os.Getenv("SERPER_API_KEY")
	if apiKey == "" {
		t.Skip("SERPER_API_KEY not set")
	}

	p, err := newSerperProvider(apiKey)
	if err != nil {
		t.Fatalf("newSerperProvider: %v", err)
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

// --- parseSerperDate (relative + absolute formats) ---

func TestParseSerperDate(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   string
		want time.Time
		ok   bool
	}{
		// Absolute (regression)
		{"iso date", "2026-01-05", time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), true},
		{"jan-day-year", "Jan 5, 2026", time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), true},
		{"rfc3339", "2026-05-19T10:30:00Z", time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC), true},

		// Day month year, French and English (Serper hl=fr, mirror-rxd)
		{"fr abbrev", "8 oct. 2025", time.Date(2025, 10, 8, 0, 0, 0, 0, time.UTC), true},
		{"fr janv", "1 janv. 2025", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"fr 1er", "1er févr. 2024", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), true},
		{"fr août", "15 août 2025", time.Date(2025, 8, 15, 0, 0, 0, 0, time.UTC), true},
		{"fr full", "28 octobre 2026", time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), true},
		{"en day month", "8 Oct 2025", time.Date(2025, 10, 8, 0, 0, 0, 0, time.UTC), true},
		{"bad day", "31 nov. 2025", time.Time{}, false},
		{"bad month", "8 foo. 2025", time.Time{}, false},

		// English relative
		{"yesterday", "yesterday", now.AddDate(0, 0, -1), true},
		{"today", "today", now, true},
		{"2 days ago", "2 days ago", now.AddDate(0, 0, -2), true},
		{"1 day ago", "1 day ago", now.AddDate(0, 0, -1), true},
		{"an hour ago", "an hour ago", now.Add(-time.Hour), true},
		{"a week ago", "a week ago", now.AddDate(0, 0, -7), true},
		{"3 weeks ago", "3 weeks ago", now.AddDate(0, 0, -21), true},
		{"1 month ago", "1 month ago", now.AddDate(0, -1, 0), true},
		{"one year ago", "one year ago", now.AddDate(-1, 0, 0), true},
		{"YESTERDAY upper", "YESTERDAY", now.AddDate(0, 0, -1), true},

		// French relative
		{"hier", "hier", now.AddDate(0, 0, -1), true},
		{"aujourd'hui", "aujourd'hui", now, true},
		{"aujourdhui no apostrophe", "aujourdhui", now, true},
		{"il y a 2 jours", "il y a 2 jours", now.AddDate(0, 0, -2), true},
		{"il y a 1 jour", "il y a 1 jour", now.AddDate(0, 0, -1), true},
		{"il y a une heure", "il y a une heure", now.Add(-time.Hour), true},
		{"il y a 3 semaines", "il y a 3 semaines", now.AddDate(0, 0, -21), true},
		{"il y a 1 mois", "il y a 1 mois", now.AddDate(0, -1, 0), true},
		{"il y a une année", "il y a une année", now.AddDate(-1, 0, 0), true},
		{"il y a un an", "il y a un an", now.AddDate(-1, 0, 0), true},

		// Garbage / unparseable
		{"empty", "", time.Time{}, false},
		{"random", "not a date", time.Time{}, false},
		{"il y a 3 quelque chose", "il y a 3 quelque chose", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSerperDate(tc.in, now)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v (got=%v)", ok, tc.ok, got)
			}
			if ok && !got.Equal(tc.want) {
				t.Fatalf("got %s want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
