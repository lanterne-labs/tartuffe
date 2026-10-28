package extractor

import (
	"bytes"
	"context"
	"fmt"
	nurl "net/url"
	"strings"
	"time"

	"digital-mirror/backend/webfetch"

	"github.com/markusmobius/go-trafilatura"
	"github.com/rs/zerolog/log"
)

const (
	// Below this, treat the extraction as a failure so the failover layer can
	// try another extractor. Anonymous fetches of paywalled pages often return
	// a few hundred chars of cookie/consent banner; a video-only stub is
	// similarly short. Either way the LLM has nothing to chew on.
	localMinContentChars = 500
	// localFetchTimeout bounds one article, with the site's pace and retries.
	localFetchTimeout = 2 * time.Minute
)

// localExtractor fetches HTML directly through webfetch (the site's own
// fetch: for most hosts the default site, which paces each domain and tries
// another browser on a refusal) and extracts the main article body locally
// with go-trafilatura. No API key, no network round-trip to a scraping
// service — but it can't bypass Cloudflare, paywalls, or aggressive
// anti-bot. Pair it with a fallback extractor via failoverExtractor for those.
type localExtractor struct{}

// newLocalExtractor returns a ready-to-use localExtractor. Unlike the other
// extractors, no env config is required — it's always available.
func newLocalExtractor() *localExtractor {
	return &localExtractor{}
}

// ExtractContent implements ContentExtractor.
func (e *localExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	parsedURL, err := nurl.ParseRequestURI(url)
	if err != nil {
		return nil, fmt.Errorf("local extractor: invalid url %q: %w", url, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localFetchTimeout)
	defer cancel()
	return e.fetchAndExtract(ctx, url, parsedURL)
}

// fetchAndExtract fetches the page and extracts its article.
func (e *localExtractor) fetchAndExtract(ctx context.Context, url string, parsedURL *nurl.URL) (*ExtractedContent, error) {
	log.Debug().Str("url", url).Msg("LocalExtractor: fetching")
	page, err := webfetch.Fetch(ctx, webfetch.Request{URL: url, Source: "news", Purpose: "article", MaxBytes: 8 << 20})
	if err != nil {
		return nil, fmt.Errorf("local extractor: %w", err)
	}
	body := page.Body

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
		Content: stripTrailingSidebarSections(stripLeadBeforeTitle(content, result.Metadata.Title)), // the ticker above the title, the sidebar below the body
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
