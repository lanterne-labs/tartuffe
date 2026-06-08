package archiver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"digital-mirror/backend/storage"
)

// fakeStorer records archive updates in memory.
type fakeStorer struct {
	mu      sync.Mutex
	sources []storage.SourceToArchive
	updates map[int64]*string // sourceID -> archiveURL passed to UpdateSourceArchiveURL
}

func (f *fakeStorer) GetSourcesWithoutArchiveURL(_ context.Context, limit int) ([]storage.SourceToArchive, error) {
	if len(f.sources) > limit {
		return f.sources[:limit], nil
	}
	return f.sources, nil
}

func (f *fakeStorer) UpdateSourceArchiveURL(_ context.Context, sourceID int64, archiveURL *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updates == nil {
		f.updates = map[int64]*string{}
	}
	f.updates[sourceID] = archiveURL
	return nil
}

// roundTripFunc serves canned responses for archive.org endpoints.
type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// newTestArchiver builds an Archiver with the given fake transport and zero delays.
func newTestArchiver(storer Storer, rt roundTripFunc) *Archiver {
	a := New(storer)
	a.client = &http.Client{Transport: rt}
	a.requestDelay = 0
	a.recheckDelay = 0
	return a
}

func TestProcessBatch_ExistingSnapshot(t *testing.T) {
	storer := &fakeStorer{sources: []storage.SourceToArchive{{ID: 1, SourceURL: "https://example.com/a"}}}
	const snap = "https://web.archive.org/web/2026/https://example.com/a"

	a := newTestArchiver(storer, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.String(), "wayback/available") {
			return jsonResp(`{"archived_snapshots":{"closest":{"available":true,"url":"` + snap + `"}}}`)
		}
		t.Errorf("unexpected save request when snapshot already exists: %s", r.URL)
		return jsonResp(`{}`)
	})

	if err := a.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	got := storer.updates[1]
	if got == nil || *got != snap {
		t.Fatalf("expected archive URL %q, got %v", snap, got)
	}
}

func TestProcessBatch_SaveThenAvailable(t *testing.T) {
	storer := &fakeStorer{sources: []storage.SourceToArchive{{ID: 2, SourceURL: "https://example.com/b"}}}
	const snap = "https://web.archive.org/web/2026/https://example.com/b"
	availCalls := 0

	a := newTestArchiver(storer, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.String(), "wayback/available") {
			availCalls++
			if availCalls == 1 {
				// First check: not yet archived.
				return jsonResp(`{"archived_snapshots":{}}`)
			}
			// Re-check after save: now available.
			return jsonResp(`{"archived_snapshots":{"closest":{"available":true,"url":"` + snap + `"}}}`)
		}
		// Save endpoint.
		return jsonResp(`ok`)
	})

	if err := a.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if availCalls != 2 {
		t.Fatalf("expected 2 availability checks (initial + recheck), got %d", availCalls)
	}
	got := storer.updates[2]
	if got == nil || *got != snap {
		t.Fatalf("expected archive URL %q after save, got %v", snap, got)
	}
}

func TestProcessBatch_SaveNotYetIndexed(t *testing.T) {
	storer := &fakeStorer{sources: []storage.SourceToArchive{{ID: 3, SourceURL: "https://example.com/c"}}}

	a := newTestArchiver(storer, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.String(), "wayback/available") {
			return jsonResp(`{"archived_snapshots":{}}`) // never indexed
		}
		return jsonResp(`ok`)
	})

	if err := a.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	// Must still stamp the attempt with a nil archive URL (so it is not retried immediately).
	got, ok := storer.updates[3]
	if !ok {
		t.Fatal("expected UpdateSourceArchiveURL to be called even when not indexed")
	}
	if got != nil {
		t.Fatalf("expected nil archive URL when snapshot not indexed, got %v", *got)
	}
}

func TestProcessBatch_ContextCancelled(t *testing.T) {
	storer := &fakeStorer{sources: []storage.SourceToArchive{{ID: 4, SourceURL: "https://example.com/d"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before processing

	a := newTestArchiver(storer, func(r *http.Request) *http.Response {
		t.Error("no HTTP request expected when context already cancelled")
		return jsonResp(`{}`)
	})

	if err := a.processBatch(ctx); err == nil {
		t.Fatal("expected context error")
	}
	if len(storer.updates) != 0 {
		t.Fatalf("expected no updates on cancelled context, got %d", len(storer.updates))
	}
}
