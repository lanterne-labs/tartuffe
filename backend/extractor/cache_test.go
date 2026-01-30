package extractor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockExtractor is a simple mock implementation for testing
type mockExtractor struct {
	callCount int
	content   *ExtractedContent
	err       error
}

func (m *mockExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	return m.content, nil
}

func TestCacheExtractor_CacheHit(t *testing.T) {
	// Create temp directory for cache
	cacheDir, err := os.MkdirTemp("", "cache-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(cacheDir)

	publishedDate := time.Date(2026, 1, 20, 10, 0, 0, 0, time.UTC)
	mockContent := &ExtractedContent{
		URL:           "https://example.com/article",
		Title:         "Test Article",
		Content:       "This is the article content.",
		DatePublished: &publishedDate,
	}

	mock := &mockExtractor{content: mockContent}
	cache := NewCacheExtractor(mock, cacheDir, true, true)

	// First call should go to the mock
	content1, err := cache.ExtractContent("https://example.com/article")
	if err != nil {
		t.Fatalf("first extraction failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call to mock, got %d", mock.callCount)
	}
	if content1.Title != "Test Article" {
		t.Errorf("expected title 'Test Article', got '%s'", content1.Title)
	}

	// Second call should come from cache
	content2, err := cache.ExtractContent("https://example.com/article")
	if err != nil {
		t.Fatalf("second extraction failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected still 1 call to mock (cache hit), got %d", mock.callCount)
	}
	if content2.Title != "Test Article" {
		t.Errorf("expected title 'Test Article', got '%s'", content2.Title)
	}
}

func TestCacheExtractor_CacheOnly(t *testing.T) {
	// Create temp directory for cache
	cacheDir, err := os.MkdirTemp("", "cache-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(cacheDir)

	// Create a cache-only extractor (no inner)
	cache := NewCacheOnlyExtractor(cacheDir)

	// Should fail because nothing in cache
	_, err = cache.ExtractContent("https://example.com/missing")
	if err == nil {
		t.Error("expected error for cache miss in cache-only mode")
	}

	// Manually write a cache file
	publishedDate := time.Date(2026, 1, 20, 10, 0, 0, 0, time.UTC)
	mockContent := &ExtractedContent{
		URL:           "https://example.com/cached",
		Title:         "Cached Article",
		Content:       "Cached content",
		DatePublished: &publishedDate,
	}

	// Use a regular cache extractor to write the file
	writeCache := NewCacheExtractor(&mockExtractor{content: mockContent}, cacheDir, true, false)
	_, err = writeCache.ExtractContent("https://example.com/cached")
	if err != nil {
		t.Fatalf("failed to populate cache: %v", err)
	}

	// Now cache-only should find it
	content, err := cache.ExtractContent("https://example.com/cached")
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if content.Title != "Cached Article" {
		t.Errorf("expected title 'Cached Article', got '%s'", content.Title)
	}
}

func TestCacheExtractor_UrlToFilename(t *testing.T) {
	cache := NewCacheExtractor(nil, "/tmp/cache", false, false)

	// Same URL should produce same filename
	filename1 := cache.urlToFilename("https://example.com/article")
	filename2 := cache.urlToFilename("https://example.com/article")
	if filename1 != filename2 {
		t.Errorf("same URL produced different filenames: %s vs %s", filename1, filename2)
	}

	// Different URLs should produce different filenames
	filename3 := cache.urlToFilename("https://example.com/other")
	if filename1 == filename3 {
		t.Errorf("different URLs produced same filename: %s", filename1)
	}

	// Filename should end with .json
	if filepath.Ext(filename1) != ".json" {
		t.Errorf("expected .json extension, got %s", filepath.Ext(filename1))
	}

	// Filename should be 64 hex chars + .json (SHA256 = 64 hex chars)
	if len(filename1) != 64+5 { // 64 hex + ".json"
		t.Errorf("expected filename length 69 (64 hex + .json), got %d", len(filename1))
	}
}

func TestCacheExtractor_ReadWriteDisabled(t *testing.T) {
	// Create temp directory for cache
	cacheDir, err := os.MkdirTemp("", "cache-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(cacheDir)

	publishedDate := time.Date(2026, 1, 20, 10, 0, 0, 0, time.UTC)
	mockContent := &ExtractedContent{
		URL:           "https://example.com/article",
		Title:         "Test Article",
		Content:       "This is the article content.",
		DatePublished: &publishedDate,
	}

	// Write-only (read disabled)
	mock := &mockExtractor{content: mockContent}
	writeOnly := NewCacheExtractor(mock, cacheDir, true, false)

	_, err = writeOnly.ExtractContent("https://example.com/article")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}

	// Second call should still go to mock (read disabled)
	_, err = writeOnly.ExtractContent("https://example.com/article")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls (read disabled), got %d", mock.callCount)
	}

	// Read-only extractor should find the cached file
	readOnly := NewCacheExtractor(nil, cacheDir, false, true)
	content, err := readOnly.ExtractContent("https://example.com/article")
	if err != nil {
		t.Fatalf("cache read failed: %v", err)
	}
	if content.Title != "Test Article" {
		t.Errorf("expected title 'Test Article', got '%s'", content.Title)
	}
}
