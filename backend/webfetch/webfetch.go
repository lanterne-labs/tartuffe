// Package webfetch is the way out to the sites we read (news articles,
// vie-publique, the Assemblée nationale, photos). Each site owns how it is
// fetched: a Site gets the shared Client and the Request, and does what the
// site needs (a login, a cookie step, its own pace, its own retries). A host
// no plugin registered goes to the default site, a polite plain GET.
//
// What every site shares goes through Client.Do: the common browser headers,
// one cookie jar, and the record of every request (site, status, duration,
// error) that the Grafana Sources page reads.
package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Request is what a call asks a site for, with what the site may need to do
// it.
type Request struct {
	URL string
	// Source is the plugin asking ("news", "viepublique", "assemblee",
	// "photos"): the label of the Sources page.
	Source string
	// Purpose is what the page is for ("article", "speech", "index",
	// "photo"), for a site that fetches them differently.
	Purpose string
	// Person is the target the page is fetched for, when there is one.
	Person string
	// Since and Until are the period of the task, when there is one.
	Since, Until time.Time
	// Header is added to the request (a Range, an Accept).
	Header http.Header
	// MaxBytes caps the body read (0 = 16 MB).
	MaxBytes int64
}

// Page is a fetched page.
type Page struct {
	URL    string // the final URL, after the redirects
	Status int
	Header http.Header
	Body   []byte
}

// Site fetches the pages of its hosts. One Site value serves the whole
// process: it can keep its own state (a session, its pace).
type Site interface {
	// Hosts are the registrable domains of the site ("vie-publique.fr"); a
	// subdomain goes to the same site.
	Hosts() []string
	Fetch(ctx context.Context, c Client, r Request) (*Page, error)
}

// Client sends a request with the common headers (the ones the request does
// not set) and the shared cookie jar, and records its outcome.
type Client interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
	// Note records an outcome of a page that is not an HTTP status: the page
	// was empty, too thin, behind a paywall, a check the site could not pass.
	Note(ctx context.Context, rawURL, outcome string)
}

// ErrGone marks a page that does not exist (404, 410): final, not retried.
var ErrGone = errors.New("page gone")

// HTTPError is a status a site could not get past.
type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status) }

var (
	mu      sync.RWMutex
	sites   = map[string]Site{}
	shared  *client
	initOne sync.Once
)

// Register makes s the site of its hosts. Plugins call it from init.
func Register(s Site) {
	mu.Lock()
	defer mu.Unlock()
	for _, h := range s.Hosts() {
		sites[strings.ToLower(h)] = s
	}
}

// Shared returns the process' Client.
func Shared() Client {
	initOne.Do(func() {
		jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		shared = &client{http: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
			},
		}}
	})
	return shared
}

// Fetch hands r to the site of its host.
func Fetch(ctx context.Context, r Request) (*Page, error) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("webfetch: invalid url %q", r.URL)
	}
	return siteOf(u.Hostname()).Fetch(withSource(ctx, r.Source), Shared(), r)
}

func siteOf(host string) Site {
	mu.RLock()
	defer mu.RUnlock()
	if s, ok := sites[strings.ToLower(host)]; ok {
		return s
	}
	if s, ok := sites[Domain(host)]; ok {
		return s
	}
	return defaultSite
}

// Domain is the registrable domain of a host (www.lemonde.fr → lemonde.fr):
// the unit of the Sources page and of the default pace.
func Domain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// Get is the plain GET of a site: the request built from r, sent through c,
// the body read. A status other than 2xx is an *HTTPError, ErrGone for 404
// and 410; the body is returned with it.
func Get(ctx context.Context, c Client, r Request) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("webfetch: %w", err)
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	max := r.MaxBytes
	if max <= 0 {
		max = 16 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", r.URL, err)
	}
	p := &Page{URL: resp.Request.URL.String(), Status: resp.StatusCode, Header: resp.Header, Body: body}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return p, fmt.Errorf("%s: HTTP %d: %w", r.URL, resp.StatusCode, ErrGone)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return p, &HTTPError{URL: r.URL, Status: resp.StatusCode}
	}
	return p, nil
}

// Retryable reports whether a failure of a fetch can pass on a later try:
// the site asked to slow down (429, 503), a server error, a timeout or a
// network error. A status the site means (403, 404) is not.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, ErrGone) || errors.Is(err, context.Canceled) {
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	return true
}

// RetryAfter reads the Retry-After header of a page, in seconds or as a
// date; 0 when absent.
func RetryAfter(p *Page) time.Duration {
	if p == nil {
		return 0
	}
	v := p.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
