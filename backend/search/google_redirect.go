package search

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"

	"digital-mirror/backend/webfetch"

	"github.com/rs/zerolog/log"
)

// Since 2026-10-30 Google lets only the first results of a search be opened
// directly; the links after them come back from the search APIs as
// google.com/goto redirects (Serper's notice of 2026-10-31). Stored as they
// came, 579 fact sources pointed at google.com instead of the article
// (review of 2026-10-31). The redirect answers a browser's request with the
// article's URL in Location, so each such link is resolved before the
// result is returned, through webfetch (the headers and cookies of a
// browser: from 2026-11-06 a bare request from the worker got a page of
// Google instead, and 516 sources were stored under the redirect). A link
// that does not resolve is kept as it is; the fetch of the page follows it,
// and its fallback reads the page at the URL it was redirected to.

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

// redirectResolver resolves Google redirect links; redirect is
// webfetch.Redirect (a test server's in the tests).
type redirectResolver struct {
	redirect func(ctx context.Context, source, link string) (string, error)
}

var googleResolver = &redirectResolver{redirect: webfetch.Redirect}

// resolve returns the URL a Google redirect link points at, or an error.
func (r *redirectResolver) resolve(ctx context.Context, link string) (string, error) {
	final, err := r.redirect(ctx, "search", link)
	if err != nil {
		return "", err
	}
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
