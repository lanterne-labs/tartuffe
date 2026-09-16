package extractor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// failoverExtractor tries a list of ContentExtractors in order and returns
// the first successful extraction. Use this to chain a cheap-but-fragile
// extractor (localExtractor) in front of a slower/paid one (WebcrawlerAPI):
// most URLs succeed locally, the fallback only fires when local fails.
type failoverExtractor struct {
	extractors []namedExtractor
}

type namedExtractor struct {
	name string
	ext  ContentExtractor
}

// newFailoverExtractor builds a failoverExtractor. Pass extractors in order
// of preference — the first one that succeeds wins. nil entries are skipped
// so callers can pass optional extractors (e.g. WebcrawlerAPI only if its
// key is set) without conditional plumbing.
func newFailoverExtractor(entries ...failoverEntry) (*failoverExtractor, error) {
	out := &failoverExtractor{}
	for _, e := range entries {
		if e.Extractor == nil {
			continue
		}
		name := e.Name
		if name == "" {
			name = "unnamed"
		}
		out.extractors = append(out.extractors, namedExtractor{name: name, ext: e.Extractor})
	}
	if len(out.extractors) == 0 {
		return nil, errors.New("failover extractor: no extractors provided")
	}
	return out, nil
}

// failoverEntry pairs an extractor with a human-readable name used in logs.
type failoverEntry struct {
	Name      string
	Extractor ContentExtractor
}

// ExtractContent tries each extractor in order. Returns the first successful
// result. If all fail, returns a wrapped error listing every failure.
func (e *failoverExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	var failures []string
	for i, n := range e.extractors {
		result, err := n.ext.ExtractContent(url)
		if err == nil {
			if i > 0 {
				log.Info().Str("url", url).Str("winner", n.name).Int("attempt", i+1).Msg("Failover extractor: fell through to backup")
			} else {
				log.Debug().Str("url", url).Str("winner", n.name).Msg("Failover extractor: primary succeeded")
			}
			return result, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", n.name, err))
		log.Warn().Err(err).Str("url", url).Str("extractor", n.name).Int("attempt", i+1).Int("remaining", len(e.extractors)-i-1).Msg("Failover extractor: attempt failed, trying next")
	}
	return nil, fmt.Errorf("failover extractor: all %d extractors failed for %s — %s",
		len(e.extractors), url, strings.Join(failures, " | "))
}
