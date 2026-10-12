package frpolitics

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestCacheFilename_DifferentURLsSameBasename(t *testing.T) {
	url16 := "https://data.assemblee-nationale.fr/static/openData/repository/16/scrutins/Scrutins.json.zip"
	url17 := "https://data.assemblee-nationale.fr/static/openData/repository/17/scrutins/Scrutins.json.zip"

	name16 := cacheFilename(url16)
	name17 := cacheFilename(url17)

	if name16 == name17 {
		t.Fatalf("cache filenames should differ for different URLs, got %q for both", name16)
	}

	// Both should end with the original basename
	if filepath.Ext(name16) != ".zip" {
		t.Errorf("expected .zip extension, got %q", name16)
	}
	if filepath.Ext(name17) != ".zip" {
		t.Errorf("expected .zip extension, got %q", name17)
	}
}

func TestCacheFilename_NoBasename(t *testing.T) {
	name := cacheFilename("https://example.com/")
	if name == "" {
		t.Fatal("expected non-empty filename")
	}
}

func TestDownloadCached_TTLExpiry(t *testing.T) {
	// Set up a test HTTP server
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("fresh data"))
	}))
	defer srv.Close()

	// Override httpClient for test
	origClient := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = origClient }()

	tmpDir := t.TempDir()
	ctx := context.Background()

	// First download — should hit server
	data, err := downloadCached(ctx, srv.URL+"/test.zip", tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("first download failed: %v", err)
	}
	if string(data) != "fresh data" {
		t.Fatalf("expected 'fresh data', got %q", string(data))
	}
	if callCount != 1 {
		t.Fatalf("expected 1 server call, got %d", callCount)
	}

	// Second download — should use cache (within TTL)
	data, err = downloadCached(ctx, srv.URL+"/test.zip", tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("second download failed: %v", err)
	}
	if string(data) != "fresh data" {
		t.Fatalf("expected 'fresh data', got %q", string(data))
	}
	if callCount != 1 {
		t.Fatalf("expected cache hit (1 call), got %d calls", callCount)
	}

	// Backdate the cache file to make it stale
	cacheName := cacheFilename(srv.URL + "/test.zip")
	cachePath := filepath.Join(tmpDir, cacheName)
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, staleTime, staleTime); err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}

	// Third download — cache expired, should re-download
	data, err = downloadCached(ctx, srv.URL+"/test.zip", tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("third download failed: %v", err)
	}
	if string(data) != "fresh data" {
		t.Fatalf("expected 'fresh data', got %q", string(data))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 server calls after TTL expiry, got %d", callCount)
	}
}

func TestDownloadCached_StaleFallback(t *testing.T) {
	// Server that fails on second call
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount > 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("original data"))
	}))
	defer srv.Close()

	origClient := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = origClient }()

	tmpDir := t.TempDir()
	ctx := context.Background()

	// First download succeeds
	data, err := downloadCached(ctx, srv.URL+"/test.zip", tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("first download failed: %v", err)
	}
	if string(data) != "original data" {
		t.Fatalf("expected 'original data', got %q", string(data))
	}

	// Backdate to make stale
	cacheName := cacheFilename(srv.URL + "/test.zip")
	cachePath := filepath.Join(tmpDir, cacheName)
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, staleTime, staleTime); err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}

	// Second download — server returns 429, should fall back to stale cache
	data, err = downloadCached(ctx, srv.URL+"/test.zip", tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("expected stale fallback, got error: %v", err)
	}
	if string(data) != "original data" {
		t.Fatalf("expected stale 'original data', got %q", string(data))
	}
}

func TestDownloadCached_ZeroMaxAge(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	origClient := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = origClient }()

	tmpDir := t.TempDir()
	ctx := context.Background()

	// Download once
	_, err := downloadCached(ctx, srv.URL+"/test.zip", tmpDir, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Backdate the file
	cacheName := cacheFilename(srv.URL + "/test.zip")
	cachePath := filepath.Join(tmpDir, cacheName)
	staleTime := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(cachePath, staleTime, staleTime)

	// With maxAge=0, should still use cache regardless of age
	_, err = downloadCached(ctx, srv.URL+"/test.zip", tmpDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected cache hit with maxAge=0, got %d calls", callCount)
	}
}

// A body read that breaks mid-transfer is retried like a failed connection:
// the first response is truncated (Content-Length larger than what is sent,
// then the handler aborts), the second is complete.
func TestDownloadCached_RetriesTruncatedBody(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("partial"))
			panic(http.ErrAbortHandler)
		}
		w.Write([]byte("complete data"))
	}))
	defer srv.Close()

	origClient := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = origClient }()

	data, err := downloadCached(context.Background(), srv.URL+"/archive.zip", t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("expected the retry to succeed, got: %v", err)
	}
	if string(data) != "complete data" || callCount != 2 {
		t.Fatalf("got %q after %d calls, want the complete body from the second call", data, callCount)
	}
}

// startResumableServer starts an httptest server with handler, and makes
// downloadResumable use its client and a backoff that does not wait.
func startResumableServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	origClient, origBackoff := httpClient, resumeBackoff
	httpClient = srv.Client()
	resumeBackoff = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { httpClient, resumeBackoff = origClient, origBackoff })
	return srv
}

// breakAfter sends the headers of the full body and n bytes of it, then
// breaks the connection.
func breakAfter(w http.ResponseWriter, body []byte, n int, etag string) {
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body[:n])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

// assertDownloaded checks that dest holds want and that the part file and its
// sidecar are removed.
func assertDownloaded(t *testing.T, dest string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %d bytes, want the %d bytes of the body", len(got), len(want))
	}
	assertNoPart(t, dest)
}

// assertNoPart checks that the part file and its sidecar of dest do not exist.
func assertNoPart(t *testing.T, dest string) {
	t.Helper()
	for _, p := range []string{dest + ".part", dest + ".part" + partMetaSuffix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must not remain, stat: %v", p, err)
		}
	}
}

// The first response breaks after half of the body. The second attempt asks
// for the rest with a Range request and the If-Range of the ETag, and the
// file holds the full body.
func TestDownloadResumable_ContinuesBrokenTransfer(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	half := len(body) / 2
	var ranges, ifRanges []string
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		ifRanges = append(ifRanges, r.Header.Get("If-Range"))
		if len(ranges) == 1 {
			breakAfter(w, body, half, `"v1"`)
		}
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(body))
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatalf("expected the resumed download to succeed, got: %v", err)
	}
	assertDownloaded(t, dest, body)
	if want := []string{"", fmt.Sprintf("bytes=%d-", half)}; !slices.Equal(ranges, want) {
		t.Fatalf("Range headers = %q, want %q", ranges, want)
	}
	if want := []string{"", `"v1"`}; !slices.Equal(ifRanges, want) {
		t.Fatalf("If-Range headers = %q, want %q", ifRanges, want)
	}
}

// A server that ignores the Range request sends the full body with 200. The
// part file starts again at byte 0, so the bytes of the first attempt do not
// stay in front of the body.
func TestDownloadResumable_FullBodyToRangeRequest(t *testing.T) {
	body := bytes.Repeat([]byte("abcdefghij"), 1000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			breakAfter(w, body, len(body)/2, `"v1"`)
		}
		if r.Header.Get("Range") == "" {
			t.Errorf("attempt %d sent no Range header", calls)
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write(body)
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, body)
	if calls != 2 {
		t.Fatalf("got %d calls, want 2", calls)
	}
}

// writePart writes a part file of dest with data and the sidecar meta.
func writePart(t *testing.T, dest string, data []byte, meta string) {
	t.Helper()
	if err := os.WriteFile(dest+".part", data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".part"+partMetaSuffix, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A part file that holds the full body gets 416 with the same size: the part
// file is the download.
func TestDownloadResumable_RangeNotSatisfiableComplete(t *testing.T) {
	body := bytes.Repeat([]byte("klmnopqrst"), 1000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(body))
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	writePart(t, dest, body, `etag "v1"`)
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, body)
	if calls != 1 {
		t.Fatalf("got %d calls, want 1", calls)
	}
}

// A part file larger than the file on the server gets 416 with a different
// size: the part file is removed and the download starts again at byte 0.
func TestDownloadResumable_RangeNotSatisfiableTooLarge(t *testing.T) {
	body := bytes.Repeat([]byte("uvwxyz0123"), 1000)
	var ranges []string
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(body))
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	writePart(t, dest, append(bytes.Clone(body), "extra"...), `etag "v1"`)
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, body)
	if want := []string{fmt.Sprintf("bytes=%d-", len(body)+5), ""}; !slices.Equal(ranges, want) {
		t.Fatalf("Range headers = %q, want %q", ranges, want)
	}
}

// A 206 whose Content-Range does not start at the offset of the part file
// cannot be appended: the part file is removed and the download starts again
// at byte 0.
func TestDownloadResumable_ContentRangeAtWrongOffset(t *testing.T) {
	body := bytes.Repeat([]byte("4567890abc"), 1000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			breakAfter(w, body, len(body)/2, `"v1"`)
		case 2:
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body)
		default:
			w.Header().Set("ETag", `"v1"`)
			http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(body))
		}
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, body)
	if calls != 3 {
		t.Fatalf("got %d calls, want 3", calls)
	}
}

// The file changes on the server between 2 attempts, with a new ETag and the
// same size. If-Range makes the server send the new file whole, and the
// result holds no byte of the old file.
func TestDownloadResumable_ChangedETagDoesNotStitch(t *testing.T) {
	oldBody := bytes.Repeat([]byte("A"), 10000)
	newBody := bytes.Repeat([]byte("B"), 10000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			breakAfter(w, oldBody, len(oldBody)/2, `"v1"`)
		}
		w.Header().Set("ETag", `"v2"`)
		http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(newBody))
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, newBody)
}

// The file changes on the server between 2 attempts, and the server sends no
// ETag and no Last-Modified. The total of Content-Range differs from the
// saved Content-Length, so the part file starts again at byte 0.
func TestDownloadResumable_ChangedSizeWithoutValidatorDoesNotStitch(t *testing.T) {
	oldBody := bytes.Repeat([]byte("A"), 10000)
	newBody := bytes.Repeat([]byte("B"), 12000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			breakAfter(w, oldBody, len(oldBody)/2, "")
		}
		http.ServeContent(w, r, "archive.zip", time.Time{}, bytes.NewReader(newBody))
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err != nil {
		t.Fatal(err)
	}
	assertDownloaded(t, dest, newBody)
	if calls != 3 {
		t.Fatalf("got %d calls, want 3 (partial, size mismatch, full)", calls)
	}
}

// A status that a retry cannot fix stops the download at once, with no file.
func TestDownloadResumable_PermanentStatus(t *testing.T) {
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err == nil {
		t.Fatal("expected an error for a 404")
	}
	if calls != 1 {
		t.Fatalf("got %d calls, want 1", calls)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("no file must exist after a failed download, stat: %v", err)
	}
	assertNoPart(t, dest)
}

// A permanent status after a partial first attempt removes the part file and
// its sidecar: a later run must not continue a file that the server removed.
func TestDownloadResumable_PermanentStatusRemovesPart(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			breakAfter(w, body, len(body)/2, `"v1"`)
		}
		http.NotFound(w, r)
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err == nil {
		t.Fatal("expected an error for a 404")
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want 2", calls)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("no file must exist after a failed download, stat: %v", err)
	}
	assertNoPart(t, dest)
}

// 7 failed attempts in a row with no new byte stop the download. The part
// file and its sidecar stay, so the next run continues from them.
func TestDownloadResumable_StallKeepsPart(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	half := len(body) / 2
	calls := 0
	srv := startResumableServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			breakAfter(w, body, half, `"v1"`)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	dest := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadResumable(context.Background(), srv.URL+"/archive.zip", dest); err == nil {
		t.Fatal("expected an error after the stall")
	}
	if calls != 8 {
		t.Fatalf("got %d calls, want 8 (1 partial, 7 without progress)", calls)
	}
	got, err := os.ReadFile(dest + ".part")
	if err != nil {
		t.Fatalf("the part file must stay: %v", err)
	}
	if !bytes.Equal(got, body[:half]) {
		t.Fatalf("part file has %d bytes, want the first %d bytes of the body", len(got), half)
	}
	meta, err := os.ReadFile(dest + ".part" + partMetaSuffix)
	if err != nil {
		t.Fatalf("the sidecar must stay: %v", err)
	}
	if string(meta) != `etag "v1"` {
		t.Fatalf("sidecar = %q, want %q", meta, `etag "v1"`)
	}
}
