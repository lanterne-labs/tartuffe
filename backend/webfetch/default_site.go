package webfetch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// genericSite is the site of every host no plugin registered: a plain GET,
// one request a second and two at once per domain, three tries. A refusal
// (401, 403) is retried with the next browser of BrowserUserAgents: many
// sites block one fingerprint and let another through.
type genericSite struct {
	paces   Paces
	timeout time.Duration
}

var defaultSite = &genericSite{
	paces:   Paces{New: func() *Pace { return &Pace{Rate: 1, Parallel: 2} }},
	timeout: 30 * time.Second,
}

func (*genericSite) Hosts() []string { return nil }

func (s *genericSite) Fetch(ctx context.Context, c Client, r Request) (*Page, error) {
	u, _ := url.Parse(r.URL)
	pace := s.paces.Of(Domain(u.Hostname()))
	var p *Page
	var err error
	for _, ua := range BrowserUserAgents {
		rr := r
		rr.Header = r.Header.Clone()
		if rr.Header == nil {
			rr.Header = http.Header{}
		}
		if rr.Header.Get("User-Agent") == "" {
			rr.Header.Set("User-Agent", ua)
		}
		p, err = Paced(ctx, pace, 3, func(ctx context.Context) (*Page, error) {
			actx, cancel := context.WithTimeout(ctx, s.timeout)
			defer cancel()
			return Get(actx, c, rr)
		})
		var he *HTTPError
		if !errors.As(err, &he) || (he.Status != http.StatusForbidden && he.Status != http.StatusUnauthorized) {
			return p, err
		}
	}
	return p, err
}
