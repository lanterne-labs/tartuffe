package extractor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	nurl "net/url"
	"strings"
	"time"

	"github.com/markusmobius/go-trafilatura"
	"github.com/rs/zerolog/log"
)

const (
	localHTTPTimeout = 25 * time.Second
	// Below this, treat the extraction as a failure so the failover layer can
	// try another extractor. Anonymous fetches of paywalled pages often return
	// a few hundred chars of cookie/consent banner; a video-only stub is
	// similarly short. Either way the LLM has nothing to chew on.
	localMinContentChars = 500
	// Realistic UA — bare "Go-http-client" UA gets challenged or blocked by a
	// non-trivial fraction of news sites.
	localUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// LocalExtractor fetches HTML directly over HTTP and extracts the main article
// body locally with go-trafilatura. No API key, no network round-trip to a
// scraping service — but it can't bypass Cloudflare, paywalls, or aggressive
// anti-bot. Pair it with a fallback extractor via FailoverExtractor for those.
type LocalExtractor struct {
	httpClient *http.Client
}

// NewLocalExtractor returns a ready-to-use LocalExtractor. Unlike the other
// extractors, no env config is required — it's always available.
func NewLocalExtractor() *LocalExtractor {
	return &LocalExtractor{
		httpClient: &http.Client{Timeout: localHTTPTimeout},
	}
}

// ExtractContent implements ContentExtractor.
func (e *LocalExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	log.Debug().Str("url", url).Msg("LocalExtractor: fetching")

	parsedURL, err := nurl.ParseRequestURI(url)
	if err != nil {
		return nil, fmt.Errorf("local extractor: invalid url %q: %w", url, err)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("local extractor: build request: %w", err)
	}
	req.Header.Set("User-Agent", localUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en;q=0.8")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local extractor: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("local extractor: %s returned HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024)) // 8MB cap
	if err != nil {
		return nil, fmt.Errorf("local extractor: read body: %w", err)
	}

	result, err := trafilatura.Extract(bytes.NewReader(body), trafilatura.Options{
		OriginalURL:     parsedURL,
		ExcludeComments: true,
		IncludeLinks:    false,
		IncludeImages:   false,
		Deduplicate:     true,
		EnableFallback:  true, // falls back to Readability/Distiller if trafilatura is unsure
	})
	if err != nil {
		return nil, fmt.Errorf("local extractor: trafilatura: %w", err)
	}

	content := strings.TrimSpace(result.ContentText)
	if len(content) < localMinContentChars {
		return nil, fmt.Errorf("local extractor: extracted only %d chars from %s (below %d-char minimum, treating as failure)",
			len(content), url, localMinContentChars)
	}

	out := &ExtractedContent{
		URL:     url,
		Title:   result.Metadata.Title,
		Content: stripTrailingSidebarSections(content), // share the same tail-trim heuristic
	}
	if !result.Metadata.Date.IsZero() {
		d := result.Metadata.Date
		out.DatePublished = &d
	}

	log.Debug().
		Str("url", url).
		Str("title", out.Title).
		Int("chars", len(out.Content)).
		Msg("LocalExtractor: success")
	return out, nil
}
