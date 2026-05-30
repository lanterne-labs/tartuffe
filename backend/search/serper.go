package search

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// SerperProvider implements NewsSearcher, WebSearcher, and ImageSearcher
// against the Serper.dev API (https://google.serper.dev).
//
// Wire format: POST + JSON body, X-API-KEY header. Supports SerpAPI-style `tbs`
// date filtering (e.g. "cdr:1,cd_min:M/D/YYYY,cd_max:M/D/YYYY") natively, so
// historical news queries work without client-side filtering.
type SerperProvider struct {
	apiKey     string
	baseURL    string // overridable in tests
	httpClient *http.Client
}

const serperDefaultBaseURL = "https://google.serper.dev"
const serperMaxPerPage = 100 // Serper accepts num up to 100

// NewSerperProvider creates a SerperProvider with the given API key.
func NewSerperProvider(apiKey string) (*SerperProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Serper API key cannot be empty")
	}
	return &SerperProvider{
		apiKey:     apiKey,
		baseURL:    serperDefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NewSerperProviderFromEnv reads SERPER_API_KEY from the environment.
func NewSerperProviderFromEnv() (*SerperProvider, error) {
	apiKey := os.Getenv("SERPER_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("SERPER_API_KEY environment variable not set")
	}
	return NewSerperProvider(apiKey)
}

// serperRequestBody mirrors the JSON body Serper accepts for /news, /search,
// and /images endpoints. Only documented fields are included.
type serperRequestBody struct {
	Query string `json:"q"`
	Num   int    `json:"num,omitempty"`
	Page  int    `json:"page,omitempty"`
	GL    string `json:"gl,omitempty"`
	HL    string `json:"hl,omitempty"`
	TBS   string `json:"tbs,omitempty"`
}

// serperNewsResponse is the news endpoint response shape.
type serperNewsResponse struct {
	News []struct {
		Title    string `json:"title"`
		Link     string `json:"link"`
		Snippet  string `json:"snippet"`
		Date     string `json:"date"`
		Source   string `json:"source"`
		ImageURL string `json:"imageUrl"`
		Position int    `json:"position"`
	} `json:"news"`
	Message string `json:"message,omitempty"` // populated on error
}

// serperWebResponse is the /search endpoint response shape.
type serperWebResponse struct {
	Organic []struct {
		Title    string `json:"title"`
		Link     string `json:"link"`
		Snippet  string `json:"snippet"`
		Date     string `json:"date"`
		Position int    `json:"position"`
	} `json:"organic"`
	Message string `json:"message,omitempty"`
}

// serperImageResponse is the /images endpoint response shape.
type serperImageResponse struct {
	Images []struct {
		Title        string `json:"title"`
		ImageURL     string `json:"imageUrl"`
		ImageWidth   int    `json:"imageWidth"`
		ImageHeight  int    `json:"imageHeight"`
		ThumbnailURL string `json:"thumbnailUrl"`
		ThumbnailW   int    `json:"thumbnailWidth"`
		ThumbnailH   int    `json:"thumbnailHeight"`
		Source       string `json:"source"`
		Domain       string `json:"domain"`
		Link         string `json:"link"`
		Position     int    `json:"position"`
	} `json:"images"`
	Message string `json:"message,omitempty"`
}

// buildTBS converts a date range to Serper's `tbs` parameter using the SerpAPI
// cdr format Serper accepts verbatim. Returns "" when both bounds are zero.
func buildSerperTBS(from, to time.Time) string {
	if from.IsZero() && to.IsZero() {
		return ""
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.AddDate(-1, 0, 0)
	}
	return fmt.Sprintf("cdr:1,cd_min:%s,cd_max:%s",
		from.Format("1/2/2006"),
		to.Format("1/2/2006"))
}

// doPost executes a POST against the given endpoint path, returning the decoded
// response body bytes. The caller is responsible for unmarshaling.
func (s *SerperProvider) doPost(endpoint string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode serper request: %w", err)
	}

	req, err := http.NewRequest("POST", s.baseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build serper request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serper request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read serper response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Surface 429s in a shape ChainedSearcher recognizes for fallthrough.
		return nil, fmt.Errorf("serper %s returned %s: %s", endpoint, resp.Status, truncate(string(respBody), 200))
	}

	return respBody, nil
}

// truncate clips a string to n chars for log/error readability.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SearchNews implements NewsSearcher.
func (s *SerperProvider) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	var all []NewsResult
	page := 1
	for len(all) < limit {
		remaining := limit - len(all)
		num := remaining
		if num > serperMaxPerPage {
			num = serperMaxPerPage
		}

		body := serperRequestBody{
			Query: query,
			Num:   num,
			Page:  page,
			TBS:   buildSerperTBS(opts.DateFrom, opts.DateTo),
		}
		if opts.Location != "" {
			body.GL = mapLocationToGLCode(opts.Location)
		}
		if opts.Language != "" {
			body.HL = opts.Language
			if body.GL == "" {
				body.GL = langToCountryGL(opts.Language)
			}
		}

		raw, err := s.doPost("/news", body)
		if err != nil {
			return nil, err
		}

		var resp serperNewsResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("decode serper news response: %w", err)
		}
		if resp.Message != "" {
			return nil, fmt.Errorf("serper news error: %s", resp.Message)
		}

		if len(resp.News) == 0 {
			break
		}

		for _, item := range resp.News {
			r := NewsResult{
				Title:   item.Title,
				Link:    item.Link,
				Snippet: item.Snippet,
				Source:  item.Source,
			}
			if t, ok := parseSerperDate(item.Date, time.Now()); ok {
				r.Date = &t
			}
			all = append(all, r)
			if len(all) >= limit {
				break
			}
		}

		if len(resp.News) < num {
			break
		}
		page++
	}

	log.Debug().Int("count", len(all)).Str("query", query).Msg("Serper news search completed")
	return all, nil
}

// PreviewSearch implements NewsSearcher.
func (s *SerperProvider) PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error) {
	previewOpts := opts
	previewOpts.Limit = 1
	results, err := s.SearchNews(query, previewOpts)
	if err != nil {
		return nil, err
	}
	return &NewsSearchPreview{
		TotalResults: len(results),
		Query:        query,
	}, nil
}

// SearchWeb implements WebSearcher.
func (s *SerperProvider) SearchWeb(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	num := limit
	if num > serperMaxPerPage {
		num = serperMaxPerPage
	}

	body := serperRequestBody{
		Query: query,
		Num:   num,
	}

	raw, err := s.doPost("/search", body)
	if err != nil {
		return nil, err
	}

	var resp serperWebResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode serper web response: %w", err)
	}
	if resp.Message != "" {
		return nil, fmt.Errorf("serper web error: %s", resp.Message)
	}

	results := make([]SearchResult, 0, len(resp.Organic))
	for i, item := range resp.Organic {
		if i >= limit {
			break
		}
		results = append(results, SearchResult{
			URL:     item.Link,
			Title:   item.Title,
			Snippet: item.Snippet,
		})
	}

	log.Debug().Int("count", len(results)).Str("query", query).Msg("Serper web search completed")
	return results, nil
}

// SearchImages implements ImageSearcher.
func (s *SerperProvider) SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	num := limit
	if num > serperMaxPerPage {
		num = serperMaxPerPage
	}

	body := serperRequestBody{
		Query: query,
		Num:   num,
	}
	if opts.Location != "" {
		body.GL = mapLocationToGLCode(opts.Location)
	}

	raw, err := s.doPost("/images", body)
	if err != nil {
		return nil, err
	}

	var resp serperImageResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode serper image response: %w", err)
	}
	if resp.Message != "" {
		return nil, fmt.Errorf("serper image error: %s", resp.Message)
	}

	results := make([]ImageResult, 0, len(resp.Images))
	for i, item := range resp.Images {
		if i >= limit {
			break
		}
		results = append(results, ImageResult{
			Original:       item.ImageURL,
			Thumbnail:      item.ThumbnailURL,
			OriginalWidth:  item.ImageWidth,
			OriginalHeight: item.ImageHeight,
			Source:         item.Source,
			Title:          item.Title,
			Position:       item.Position,
		})
	}

	log.Debug().Int("count", len(results)).Str("query", query).Msg("Serper image search completed")
	return results, nil
}

// Search implements the legacy SearchProvider interface (used by some callers).
func (s *SerperProvider) Search(personName string) ([]SearchResult, error) {
	newsResults, err := s.SearchNews(personName, NewsSearchOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(newsResults))
	for _, nr := range newsResults {
		out = append(out, SearchResult{
			URL:     nr.Link,
			Title:   nr.Title,
			Snippet: nr.Snippet,
		})
	}
	return out, nil
}

// parseSerperDate parses Serper's mixed date formats — absolute (ISO,
// "Jan 2, 2006"), English relative ("2 days ago", "yesterday"), and French
// relative ("il y a 3 jours", "hier"). Returns (parsed_time, true) on success,
// (zero, false) otherwise so callers leave NewsResult.Date as nil.
//
// `now` anchors all relative arithmetic. Tests pin a fixed `now` to keep
// assertions deterministic.
func parseSerperDate(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// Absolute formats first (cheap, common).
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02",
		"Jan 2, 2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	// Relative formats — English + French. Returns the resolved time or
	// (zero, false) if no relative pattern matched.
	if t, ok := parseSerperRelativeDate(s, now); ok {
		return t, true
	}
	return time.Time{}, false
}

// Relative-date regexes. All case-insensitive (compiled with (?i)).
//
// English: "yesterday", "today", "N (hour|day|week|month|year)s? ago",
//
//	"an? (unit) ago", "one (unit) ago"
//
// French:  "hier", "aujourd'hui", "il y a N (heure|jour|semaine|mois|an(née)?)s?",
//
//	"il y a un[e]? (unit)"
var (
	serperRelEnNumeric = regexp.MustCompile(`(?i)^\s*(\d+)\s+(hour|day|week|month|year)s?\s+ago\s*$`)
	serperRelEnArticle = regexp.MustCompile(`(?i)^\s*(?:an?|one)\s+(hour|day|week|month|year)\s+ago\s*$`)
	serperRelFrNumeric = regexp.MustCompile(`(?i)^\s*il\s+y\s+a\s+(\d+)\s+(heure|jour|semaine|mois|année|an)s?\s*$`)
	serperRelFrArticle = regexp.MustCompile(`(?i)^\s*il\s+y\s+a\s+une?\s+(heure|jour|semaine|mois|année|an)\s*$`)
)

func parseSerperRelativeDate(s string, now time.Time) (time.Time, bool) {
	low := strings.ToLower(strings.TrimSpace(s))

	// One-word forms.
	switch low {
	case "today", "aujourd'hui", "aujourdhui":
		return now, true
	case "yesterday", "hier":
		return now.AddDate(0, 0, -1), true
	}

	// Numeric forms ("3 days ago", "il y a 3 jours").
	if m := serperRelEnNumeric.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return subtractUnits(now, n, m[2]), true
	}
	if m := serperRelFrNumeric.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return subtractUnits(now, n, m[2]), true
	}

	// Singular-article forms ("an hour ago", "il y a une heure").
	if m := serperRelEnArticle.FindStringSubmatch(s); m != nil {
		return subtractUnits(now, 1, m[1]), true
	}
	if m := serperRelFrArticle.FindStringSubmatch(s); m != nil {
		return subtractUnits(now, 1, m[1]), true
	}

	return time.Time{}, false
}

// subtractUnits applies "now - n×unit" using calendar-aware arithmetic for
// week/month/year so day-of-month wrapping behaves correctly.
func subtractUnits(now time.Time, n int, unit string) time.Time {
	switch strings.ToLower(unit) {
	case "hour", "heure":
		return now.Add(-time.Duration(n) * time.Hour)
	case "day", "jour":
		return now.AddDate(0, 0, -n)
	case "week", "semaine":
		return now.AddDate(0, 0, -7*n)
	case "month", "mois":
		return now.AddDate(0, -n, 0)
	case "year", "an", "année":
		return now.AddDate(-n, 0, 0)
	}
	// Shouldn't reach here — the regexes constrain the unit set — but be safe.
	return now
}
