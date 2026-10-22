package webfetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// browserHeaders are the headers of a current desktop Chrome in French: the
// most common ones on the sites we read. A request keeps the ones it sets.
var browserHeaders = map[string]string{
	"User-Agent":                BrowserUserAgents[0],
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	"Accept-Language":           "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7",
	"Upgrade-Insecure-Requests": "1",
}

// BrowserUserAgents are current desktop browsers, Chrome first. A site that
// blocks one fingerprint and lets another through can try them in turn.
var BrowserUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:143.0) Gecko/20100101 Firefox/143.0",
}

type client struct {
	http *http.Client
}

type sourceKey struct{}

func withSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// WithSource labels the requests sent under ctx for the Sources page, for a
// caller that uses Shared().Do directly instead of Fetch.
func WithSource(ctx context.Context, source string) context.Context {
	return withSource(ctx, source)
}

func sourceOf(ctx context.Context) string {
	if s, _ := ctx.Value(sourceKey{}).(string); s != "" {
		return s
	}
	return "other"
}

func (c *client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	for k, v := range browserHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	o := Outcome{At: start, Source: sourceOf(ctx), Host: req.URL.Hostname(), Duration: time.Since(start)}
	switch {
	case err != nil:
		o.Outcome = errorKind(err)
	default:
		o.Status = resp.StatusCode
		o.Outcome = statusKind(resp.StatusCode)
	}
	record(o)
	return resp, err
}

func (c *client) Note(ctx context.Context, rawURL, outcome string) {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	record(Outcome{At: time.Now(), Source: sourceOf(ctx), Host: host, Outcome: outcome, Note: true})
}

// Note records an outcome of a page through the shared client.
func Note(ctx context.Context, source, rawURL, outcome string) {
	Shared().Note(withSource(ctx, source), rawURL, outcome)
}

func statusKind(status int) string {
	switch {
	case status >= 200 && status < 400:
		return "ok"
	case status == http.StatusNotFound || status == http.StatusGone:
		return "gone"
	case status == http.StatusTooManyRequests:
		return "throttled"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "refused"
	case status >= 500:
		return "server_error"
	}
	return "client_error"
}

func errorKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	}
	return "network"
}

// Outcome is the record of one request, or of a note on a page.
type Outcome struct {
	At       time.Time
	Source   string
	Host     string
	Status   int // 0 for a network error or a note
	Outcome  string
	Duration time.Duration
	// Note marks an outcome of the page rather than of a request.
	Note bool
}

// Recorder stores the outcomes. storage.FetchLog is the one of the
// processes.
type Recorder interface {
	Record(o Outcome)
}

var (
	recMu    sync.RWMutex
	recorder Recorder
)

// SetRecorder sets where the outcomes go (none: they are only counted in the
// debug log).
func SetRecorder(r Recorder) {
	recMu.Lock()
	recorder = r
	recMu.Unlock()
}

func record(o Outcome) {
	recMu.RLock()
	r := recorder
	recMu.RUnlock()
	if o.Outcome != "ok" {
		log.Debug().Str("source", o.Source).Str("host", o.Host).Int("status", o.Status).Str("outcome", o.Outcome).Msg("webfetch: outcome")
	}
	if r != nil {
		r.Record(o)
	}
}
