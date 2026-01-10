package frpolitics

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// getCacheDir returns the cache directory path, checking multiple locations.
// It first checks package-relative path, then common project layouts.
func getCacheDir() string {
	// First try package-relative path using runtime caller (works when source files exist)
	_, currentFile, _, ok := runtime.Caller(0)
	if ok {
		pkgDir := filepath.Dir(currentFile)
		cacheDir := filepath.Join(pkgDir, "asset", "cache")
		if info, err := os.Stat(cacheDir); err == nil && info.IsDir() {
			return cacheDir
		}
	}

	// Try common project layouts
	candidates := []string{
		"asset/cache",                                 // from plugin directory
		"backend/plugins/frpolitics/asset/cache",      // from project root
		"../backend/plugins/frpolitics/asset/cache",   // from cmd directory
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}

	// Create the package-relative directory if possible
	if ok {
		pkgDir := filepath.Dir(currentFile)
		cacheDir := filepath.Join(pkgDir, "asset", "cache")
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			return cacheDir
		}
	}

	// Last resort: use project-root relative path
	return "backend/plugins/frpolitics/asset/cache"
}


// downloadCached downloads the content at srcURL using a simple file cache.
// If a file already exists in cacheDir with the URL's basename, it is returned.
// Otherwise, the content is fetched, atomically written to the cache, and returned.
func downloadCached(ctx context.Context, srcURL string, cacheDir string) ([]byte, error) {
	parsed, err := url.Parse(srcURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", srcURL, err)
	}
	filename := path.Base(parsed.Path)
	if filename == "" || filename == "." || filename == "/" {
		// Fall back to a stable hash-based filename when no basename is present
		sum := sha1.Sum([]byte(srcURL))
		filename = "download_" + hex.EncodeToString(sum[:]) + ".bin"
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure cache dir: %w", err)
	}
	cachePath := filepath.Join(cacheDir, filename)
	// Return cached content if present
	if f, err := os.Open(cachePath); err == nil {
		defer f.Close()
		return io.ReadAll(f)
	}
	// Download with simple retry/backoff for 429/5xx
	var data []byte
	var lastErr error
	backoff := 300 * time.Millisecond
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http get: %w", err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				data, err = io.ReadAll(resp.Body)
				if err != nil {
					lastErr = fmt.Errorf("read body: %w", err)
					return
				}
				lastErr = nil
				return
			}
			// Retry on 429/5xx with Retry-After if present
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, convErr := strconv.Atoi(ra); convErr == nil && secs > 0 {
						time.Sleep(time.Duration(secs) * time.Second)
					} else {
						time.Sleep(backoff)
						backoff *= 2
					}
				} else {
					time.Sleep(backoff)
					backoff *= 2
				}
				lastErr = fmt.Errorf("retryable status %d", resp.StatusCode)
				return
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
		}()
		if lastErr == nil && len(data) > 0 {
			break
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	// Atomic write
	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("rename cache: %w", err)
	}
	return data, nil
}
