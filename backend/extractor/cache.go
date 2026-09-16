package extractor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// CacheExtractor wraps a ContentExtractor to provide disk-based caching.
// This allows extracted content to be saved and reloaded, useful for
// iterating on fact extraction prompts without re-scraping.
type CacheExtractor struct {
	inner        ContentExtractor
	cacheDir     string
	writeEnabled bool // Save new extractions to cache
	readEnabled  bool // Load from cache if available
}

// cachedContent is the JSON structure stored on disk.
type cachedContent struct {
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Content       string     `json:"content"`
	DatePublished *time.Time `json:"date_published,omitempty"`
	CachedAt      time.Time  `json:"cached_at"`
}

// NewCacheExtractor creates a CacheExtractor wrapping the given ContentExtractor.
// cacheDir is the directory where cached content will be stored.
// writeEnabled controls whether new extractions are saved to cache.
// readEnabled controls whether cache is checked before extraction.
func NewCacheExtractor(inner ContentExtractor, cacheDir string, writeEnabled, readEnabled bool) *CacheExtractor {
	return &CacheExtractor{
		inner:        inner,
		cacheDir:     cacheDir,
		writeEnabled: writeEnabled,
		readEnabled:  readEnabled,
	}
}

// NewCacheOnlyExtractor creates a CacheExtractor that only reads from cache.
// This is useful when you want to process already-cached content without
// making any network requests. Returns an error if the cache file doesn't exist.
func NewCacheOnlyExtractor(cacheDir string) *CacheExtractor {
	return &CacheExtractor{
		inner:        nil,
		cacheDir:     cacheDir,
		writeEnabled: false,
		readEnabled:  true,
	}
}

// ExtractContent implements the ContentExtractor interface.
// It first checks the cache (if readEnabled), then delegates to the inner
// extractor (if cache miss and inner is not nil), and saves to cache (if writeEnabled).
func (c *CacheExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	// Try to load from cache first
	if c.readEnabled {
		content, err := c.loadFromCache(url)
		if err == nil {
			log.Debug().Str("url", url).Msg("Cache hit - loaded from disk")
			return content, nil
		}
		// Log cache miss or read error
		if !os.IsNotExist(err) {
			log.Warn().Err(err).Str("url", url).Msg("Failed to read from cache")
		}
	}

	// Cache miss - check if we have an inner extractor
	if c.inner == nil {
		return nil, fmt.Errorf("cache miss and no inner extractor available for url: %s", url)
	}

	// Delegate to inner extractor
	log.Debug().Str("url", url).Msg("Cache miss - extracting from source")
	content, err := c.inner.ExtractContent(url)
	if err != nil {
		return nil, err
	}

	// Save to cache if enabled
	if c.writeEnabled && content != nil {
		if saveErr := c.saveToCache(url, content); saveErr != nil {
			log.Warn().Err(saveErr).Str("url", url).Msg("Failed to save to cache")
			// Don't fail the extraction because of cache write error
		} else {
			log.Debug().Str("url", url).Msg("Saved to cache")
		}
	}

	return content, nil
}

// urlToFilename converts a URL to a cache filename using SHA256 hash.
func (c *CacheExtractor) urlToFilename(url string) string {
	hash := sha256.Sum256([]byte(url))
	return hex.EncodeToString(hash[:]) + ".json"
}

// loadFromCache attempts to load cached content for the given URL.
func (c *CacheExtractor) loadFromCache(url string) (*ExtractedContent, error) {
	filename := c.urlToFilename(url)
	filepath := filepath.Join(c.cacheDir, filename)

	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	var cached cachedContent
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached content: %w", err)
	}

	return &ExtractedContent{
		URL:           cached.URL,
		Title:         cached.Title,
		Content:       cached.Content,
		DatePublished: cached.DatePublished,
	}, nil
}

// saveToCache saves the extracted content to the cache directory.
func (c *CacheExtractor) saveToCache(url string, content *ExtractedContent) error {
	// Ensure cache directory exists
	if err := os.MkdirAll(c.cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	cached := cachedContent{
		URL:           content.URL,
		Title:         content.Title,
		Content:       content.Content,
		DatePublished: content.DatePublished,
		CachedAt:      time.Now(),
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal content for cache: %w", err)
	}

	filename := c.urlToFilename(url)
	filepath := filepath.Join(c.cacheDir, filename)

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// CacheDir returns the cache directory path.
func (c *CacheExtractor) CacheDir() string {
	return c.cacheDir
}

// LoadAllCached loads all cached content from the cache directory.
// Returns a slice of ExtractedContent for all valid JSON files found.
// This is useful for cache-only mode where we want to process all cached content
// without doing a news search first.
func (c *CacheExtractor) LoadAllCached() ([]*ExtractedContent, error) {
	entries, err := os.ReadDir(c.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	var contents []*ExtractedContent
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(c.cacheDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Warn().Err(err).Str("file", entry.Name()).Msg("Failed to read cache file")
			continue
		}

		var cached cachedContent
		if err := json.Unmarshal(data, &cached); err != nil {
			log.Warn().Err(err).Str("file", entry.Name()).Msg("Failed to unmarshal cache file")
			continue
		}

		contents = append(contents, &ExtractedContent{
			URL:           cached.URL,
			Title:         cached.Title,
			Content:       cached.Content,
			DatePublished: cached.DatePublished,
		})
	}

	log.Info().Int("files_loaded", len(contents)).Str("cache_dir", c.cacheDir).Msg("Loaded all cached content")
	return contents, nil
}
