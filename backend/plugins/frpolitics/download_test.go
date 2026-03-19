package frpolitics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
