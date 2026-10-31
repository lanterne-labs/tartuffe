package search

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Since 2026-10-30 Google lets only the first results of a search be opened
// directly; the links after them come back from the search APIs as
// google.com/goto redirects (Serper's notice of 2026-10-31). Stored as they
// came, 579 fact sources pointed at google.com instead of the article
// (review of 2026-10-31). The redirect answers a plain request with the
// article's URL in Location, so each such link is resolved before the
// result is returned; a link that does not resolve is kept as it is, and
// the fetch of the page follows it anyway.

// IsGoogleRedirect reports whether link is a Google redirect (/goto or
// /url on a google host) rather than the page itself.
func IsGoogleRedirect(link string) bool { return isGoogleRedirect(link) }

// ResolveGoogleLink returns the URL a Google redirect link points at.
func ResolveGoogleLink(ctx context.Context, link string) (string, error) {
	return googleResolver.resolve(ctx, link)
}

func isGoogleRedirect(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "google.com" && !strings.HasPrefix(host, "google.") {
		return false
	}
	return u.Path == "/goto" || u.Path == "/url"
}

// redirectResolver resolves Google redirect links; it keeps its own client,
// which never follows the redirect (the Location is the answer).
type redirectResolver struct {
	client *http.Client
}

var googleResolver = &redirectResolver{client: &http.Client{
	Timeout: 8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}}

// resolve returns the URL a Google redirect link points at, or an error.
func (r *redirectResolver) resolve(ctx context.Context, link string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
		return "", errors.New("no redirect in the answer")
	}
	target, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	final := req.URL.ResolveReference(target).String()
	if isGoogleRedirect(final) {
		return "", errors.New("the redirect points at another google redirect")
	}
	return final, nil
}

// resolveGoogleLinks replaces, in place, every Google redirect link of the
// results with the URL it points at, a few at a time. links gives the
// pointer to each result's link.
func resolveGoogleLinks(ctx context.Context, links []*string) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, l := range links {
		if l == nil || !isGoogleRedirect(*l) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(l *string) {
			defer wg.Done()
			defer func() { <-sem }()
			final, err := googleResolver.resolve(ctx, *l)
			if err != nil {
				log.Warn().Err(err).Str("link", *l).Msg("search: google redirect not resolved; the link is kept")
				return
			}
			*l = final
		}(l)
	}
	wg.Wait()
}
