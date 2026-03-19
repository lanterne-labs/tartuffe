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

	"github.com/rs/zerolog/log"
)

// getCacheDir returns the cache directory path.
// It checks FRPOLITICS_CACHE_DIR env var first, then falls back to source-relative paths.
func getCacheDir() string {
	// Check env var first (used on the host with a persistent volume)
	if dir := os.Getenv("FRPOLITICS_CACHE_DIR"); dir != "" {
		return dir
	}

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


// cacheFilename returns a cache-key-safe filename for a URL.
// It prefixes the URL basename with the first 8 chars of SHA1(full URL)
// to disambiguate URLs with the same basename (e.g. different legislature paths).
func cacheFilename(srcURL string) string {
	parsed, _ := url.Parse(srcURL)
	basename := path.Base(parsed.Path)
	if basename == "" || basename == "." || basename == "/" {
		sum := sha1.Sum([]byte(srcURL))
		return "download_" + hex.EncodeToString(sum[:]) + ".bin"
	}
	sum := sha1.Sum([]byte(srcURL))
	prefix := hex.EncodeToString(sum[:])[:8]
	return prefix + "_" + basename
}

// downloadCached downloads the content at srcURL using a file cache with TTL.
// If maxAge > 0, cached files older than maxAge are considered stale and re-downloaded.
// If maxAge == 0, cached files are returned regardless of age (no expiry).
// If re-download fails but a stale cache exists, the stale content is returned with a warning.
func downloadCached(ctx context.Context, srcURL string, cacheDir string, maxAge time.Duration) ([]byte, error) {
	filename := cacheFilename(srcURL)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure cache dir: %w", err)
	}
	cachePath := filepath.Join(cacheDir, filename)

	// Check if cached file exists and is fresh enough
	var staleData []byte
	if info, err := os.Stat(cachePath); err == nil {
		isFresh := maxAge == 0 || time.Since(info.ModTime()) < maxAge
		if isFresh {
			if data, err := os.ReadFile(cachePath); err == nil {
				return data, nil
			}
		} else {
			// Stale — read it as fallback in case download fails
			staleData, _ = os.ReadFile(cachePath)
			log.Debug().
				Str("file", filename).
				Dur("age", time.Since(info.ModTime())).
				Dur("max_age", maxAge).
				Msg("frpolitics: cache expired, re-downloading")
		}
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
		// Fall back to stale cache if download failed
		if len(staleData) > 0 {
			log.Warn().Err(lastErr).Str("file", filename).Msg("frpolitics: download failed, using stale cache")
			return staleData, nil
		}
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
