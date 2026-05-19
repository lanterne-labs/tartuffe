package entityvalidator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// WikiFilter searches Wikipedia (French first, then English) for the candidate
// and fetches the intro paragraph of the top few results. It ALWAYS Defers:
// the result (possibly empty pages list) is attached as JSON in Extra so the
// LLMFilter downstream can weigh it alongside other signals (e.g. GoogleFilter).
//
// The previous "reject when zero pages" behavior produced too many false
// negatives — real but obscure entities (local politicians, French
// associations, names stored in non-Latin script) have no Wikipedia article
// but DO have a Google footprint, so the LLM gets to combine both sources
// before deciding. Empty wiki_pages in the LLM prompt is a soft negative,
// not a hard reject.
type WikiFilter struct {
	HTTP      *http.Client
	Languages []string // tried in order, e.g. ["fr", "en"]
	UserAgent string   // Wikipedia requires a descriptive UA
	MaxPages  int      // top N results to fetch intro paragraphs for (default 3)
}

// NewWikiFilter returns a WikiFilter with sane defaults.
func NewWikiFilter() *WikiFilter {
	return &WikiFilter{
		HTTP:      &http.Client{Timeout: 15 * time.Second},
		Languages: []string{"fr", "en"},
		UserAgent: "Mirror-Entity-Validator/1.0 (https://tartuffe.me; contact: noreply@tartuffe.me)",
		MaxPages:  3,
	}
}

func (w *WikiFilter) Name() string { return "wiki" }

// WikiPage is one Wikipedia article we successfully retrieved an intro for.
// It's exported because LLMFilter (and tests) parse Extra back into this shape.
type WikiPage struct {
	Lang    string `json:"lang"`    // BCP-47 language code (the Wikipedia we searched)
	Title   string `json:"title"`   // canonical article title
	Extract string `json:"extract"` // first intro paragraph in plain text
}

// wikiExtra is the structured payload WikiFilter attaches to its FilterResult.
// LLMFilter reads it via json.Unmarshal(prev.Result.Extra, &wikiExtra{}).
type wikiExtra struct {
	Pages []WikiPage `json:"pages"`
}

func (w *WikiFilter) Check(ctx context.Context, c Candidate) (FilterResult, error) {
	if c.Name == "" {
		return FilterResult{Decision: Defer}, nil
	}

	queries := []string{c.Name}
	if stripped := stripParenthetical(c.Name); stripped != "" && stripped != c.Name {
		queries = append(queries, stripped)
	}

	var allPages []WikiPage
	seen := map[string]bool{} // dedup by lang:title

	for _, lang := range w.Languages {
		for _, q := range queries {
			pages, err := w.searchWithExtracts(ctx, lang, q, w.MaxPages)
			if err != nil {
				log.Debug().Err(err).Str("lang", lang).Str("query", q).Msg("wiki search-with-extracts failed")
				continue
			}
			for _, p := range pages {
				key := p.Lang + ":" + p.Title
				if seen[key] {
					continue
				}
				seen[key] = true
				allPages = append(allPages, p)
			}
		}
		// Stop after first language that returned anything — French politicians
		// usually have a French page; falling through to English would just dilute.
		if len(allPages) > 0 {
			break
		}
	}

	// Cap the payload size so the downstream LLM prompt stays bounded.
	if len(allPages) > w.MaxPages {
		allPages = allPages[:w.MaxPages]
	}
	extra, err := json.Marshal(wikiExtra{Pages: allPages})
	if err != nil {
		// Marshaling a small struct of strings shouldn't fail; treat as soft-defer.
		return FilterResult{Decision: Defer, Reason: "wiki: extras marshal failed"}, nil
	}

	if len(allPages) == 0 {
		return FilterResult{
			Decision: Defer,
			Reason:   "wiki: no matching page in any configured language",
			Extra:    extra, // empty pages list, still valid JSON for LLM prompt
		}, nil
	}
	titles := make([]string, 0, len(allPages))
	for _, p := range allPages {
		titles = append(titles, p.Lang+":"+p.Title)
	}
	return FilterResult{
		Decision: Defer,
		Reason:   fmt.Sprintf("wiki: %d page(s) found — %s", len(allPages), strings.Join(titles, ", ")),
		Extra:    extra,
	}, nil
}

// --- Wikipedia API plumbing ---

// searchWithExtracts runs one MediaWiki call that combines `generator=search`
// with `prop=extracts` so we get the intro paragraph of the top `limit` search
// results in a single round-trip. Returns a (possibly empty) slice of pages
// ordered by the search engine's ranking — Wikipedia returns a map keyed by
// page id and an `index` field per page that we sort by.
func (w *WikiFilter) searchWithExtracts(ctx context.Context, lang, query string, limit int) ([]WikiPage, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("generator", "search")
	params.Set("gsrsearch", query)
	params.Set("gsrnamespace", "0")
	params.Set("gsrlimit", fmt.Sprintf("%d", limit))
	params.Set("prop", "extracts")
	params.Set("exintro", "1")
	params.Set("explaintext", "1")
	params.Set("exlimit", "max")
	body, err := w.get(ctx, lang, params)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Query struct {
			Pages []struct {
				PageID  int    `json:"pageid"`
				Index   int    `json:"index"`
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode wiki response: %w", err)
	}
	pages := raw.Query.Pages
	// Order by the search engine's ranking; ties default to title order.
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Index < pages[j].Index })

	out := make([]WikiPage, 0, len(pages))
	for _, p := range pages {
		extract := strings.TrimSpace(p.Extract)
		if extract == "" {
			// Disambiguation pages and some stubs come back with no extract — skip.
			continue
		}
		// Drop obvious disambiguation pages by content cue.
		lower := strings.ToLower(extract)
		if strings.Contains(lower, "peut faire référence à") ||
			strings.Contains(lower, "may refer to") {
			continue
		}
		out = append(out, WikiPage{
			Lang:    lang,
			Title:   p.Title,
			Extract: truncateExtract(extract, 400),
		})
	}
	return out, nil
}

func (w *WikiFilter) get(ctx context.Context, lang string, params url.Values) ([]byte, error) {
	u := fmt.Sprintf("https://%s.wikipedia.org/w/api.php?%s", lang, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", w.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikipedia api status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// truncateExtract keeps the prompt bounded. We cut on a word boundary so the
// LLM doesn't see a mid-token slice.
func truncateExtract(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	runes := []rune(s)
	cut := max
	// Walk back to the previous space if we'd cut mid-word.
	for cut > 0 && runes[cut] != ' ' && runes[cut] != '\n' {
		cut--
	}
	if cut == 0 {
		cut = max
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

// stripParenthetical removes "X (foo)" trailers from a name so we can search
// for the base form when the parenthetical is just a clarifier.
func stripParenthetical(s string) string {
	out := s
	for {
		open := strings.LastIndex(out, "(")
		if open < 0 {
			break
		}
		close := strings.Index(out[open:], ")")
		if close < 0 {
			break
		}
		out = strings.TrimSpace(out[:open]) + strings.TrimSpace(out[open+close+1:])
	}
	return strings.TrimSpace(out)
}
