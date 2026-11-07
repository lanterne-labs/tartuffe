package webfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memRecorder struct {
	mu  sync.Mutex
	out []Outcome
}

func (m *memRecorder) Record(o Outcome) {
	m.mu.Lock()
	m.out = append(m.out, o)
	m.mu.Unlock()
}

func (m *memRecorder) outcomes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s []string
	for _, o := range m.out {
		s = append(s, o.Source+":"+o.Outcome)
	}
	return s
}

func fastSite() *genericSite {
	return &genericSite{paces: Paces{New: func() *Pace { return &Pace{Rate: 1000, Parallel: 2} }}, timeout: 5 * time.Second}
}

// TestDefaultSite: the common headers go out, a 429 is retried after the
// pace slows down, a 403 is retried with another browser, a 404 is final,
// and every request is recorded under its source.
func TestDefaultSite(t *testing.T) {
	rec := &memRecorder{}
	SetRecorder(rec)
	defer SetRecorder(nil)

	var hits atomic.Int32
	var uas sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		uas.Store(r.Header.Get("User-Agent"), true)
		if strings.Contains(r.Header.Get("User-Agent"), "app") || r.Header.Get("Accept-Language") == "" {
			t.Errorf("headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/throttled":
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
		case "/refused":
			if r.Header.Get("User-Agent") == BrowserUserAgents[0] {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("page " + r.URL.Path))
	}))
	defer srv.Close()

	s := fastSite()
	ctx := withSource(context.Background(), "news")
	p, err := s.Fetch(ctx, Shared(), Request{URL: srv.URL + "/throttled"})
	if err != nil || string(p.Body) != "page /throttled" {
		t.Fatalf("throttled: %v %v", p, err)
	}
	if got := s.paces.Of(Domain("127.0.0.1")).Current(); got >= 1000 {
		t.Errorf("the pace did not slow down: %v", got)
	}

	p, err = s.Fetch(ctx, Shared(), Request{URL: srv.URL + "/refused"})
	if err != nil || string(p.Body) != "page /refused" {
		t.Fatalf("refused: %v %v", p, err)
	}

	hits.Store(0)
	_, err = s.Fetch(ctx, Shared(), Request{URL: srv.URL + "/gone"})
	if !errors.Is(err, ErrGone) || hits.Load() != 1 {
		t.Errorf("gone: %v after %d requests", err, hits.Load())
	}

	Shared().Note(ctx, srv.URL+"/x", "article_thin")
	got := strings.Join(rec.outcomes(), " ")
	if got != "news:throttled news:ok news:refused news:ok news:gone news:article_thin" {
		t.Errorf("recorded: %s", got)
	}
}

// siteFunc is a registered site for the test.
type siteFunc struct {
	hosts []string
	fetch func(ctx context.Context, c Client, r Request) (*Page, error)
}

func (s siteFunc) Hosts() []string { return s.hosts }
func (s siteFunc) Fetch(ctx context.Context, c Client, r Request) (*Page, error) {
	return s.fetch(ctx, c, r)
}

// TestRegister: a host and its subdomains go to the registered site, with
// the request as asked.
func TestRegister(t *testing.T) {
	var got Request
	Register(siteFunc{hosts: []string{"example-site.fr"}, fetch: func(ctx context.Context, c Client, r Request) (*Page, error) {
		got = r
		return &Page{Status: 200}, nil
	}})
	if _, err := Fetch(context.Background(), Request{URL: "https://www.example-site.fr/a", Source: "viepublique", Person: "X"}); err != nil {
		t.Fatal(err)
	}
	if got.Person != "X" || got.URL != "https://www.example-site.fr/a" {
		t.Errorf("request: %+v", got)
	}
	if d := Domain("www.lemonde.fr"); d != "lemonde.fr" {
		t.Errorf("domain: %s", d)
	}
	if d := Domain("actu.orange.fr"); d != "orange.fr" {
		t.Errorf("domain: %s", d)
	}
}

// TestPace: requests are spaced at the rate, at most Parallel at once.
func TestPace(t *testing.T) {
	p := &Pace{Rate: 20, Parallel: 2}
	var inFlight, peak atomic.Int32
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := p.Wait(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Errorf("6 requests at 20/s took %s", el)
	}
	if peak.Load() > 2 {
		t.Errorf("%d at once, want 2 at most", peak.Load())
	}
	p.Slow(0)
	if p.Current() != 10 {
		t.Errorf("after Slow: %v", p.Current())
	}
	for i := 0; i < 10; i++ {
		p.Fine()
	}
	if p.Current() != 12.5 {
		t.Errorf("after 10 fine: %v", p.Current())
	}
}

// TestRedirect: the Location of a redirect, asked with the browser headers
// and recorded; an answer that is not a redirect is an HTTPError.
func TestRedirect(t *testing.T) {
	rec := &memRecorder{}
	SetRecorder(rec)
	defer SetRecorder(nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/goto" && r.Header.Get("Accept-Language") != "" {
			http.Redirect(w, r, "https://www.lepoint.fr/a", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	final, err := Redirect(context.Background(), "search", srv.URL+"/goto?url=CAES")
	if err != nil || final != "https://www.lepoint.fr/a" {
		t.Fatalf("redirect: got %q, %v", final, err)
	}
	_, err = Redirect(context.Background(), "search", srv.URL+"/plain")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusOK {
		t.Fatalf("a page that does not redirect: got %v", err)
	}
	if got := strings.Join(rec.outcomes(), ","); got != "search:ok,search:ok" {
		t.Errorf("recorded %s", got)
	}
}
