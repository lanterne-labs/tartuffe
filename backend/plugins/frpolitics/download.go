package frpolitics

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
		"asset/cache",                               // from plugin directory
		"backend/plugins/frpolitics/asset/cache",    // from project root
		"../backend/plugins/frpolitics/asset/cache", // from cmd directory
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
// errNotFound marks a download of a page that does not exist (404).
var errNotFound = errors.New("not found")

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

	// Retry/backoff for 429/5xx. Tuned for AN's open-data portal: the previous
	// 300ms→4.8s budget was too tight (a 429 typically needs minutes to clear),
	// and combined with the two-URL-fallback in run.go we were firing ~10
	// requests in ~18s, which keeps re-triggering the limiter. Now: 7 attempts,
	// exponential 2s → 4s → 8s → 16s → 32s → 60s → 60s (capped), with ±25%
	// jitter so concurrent retries don't synchronise. Retry-After is still
	// honoured when the server sends it.
	const (
		maxAttempts    = 7
		initialBackoff = 2 * time.Second
		maxBackoff     = 60 * time.Second
	)
	sleepWithJitter := func(ctx context.Context, base time.Duration) {
		// ±25% jitter; bounded by maxBackoff.
		if base > maxBackoff {
			base = maxBackoff
		}
		jitter := time.Duration(float64(base) * (rand.Float64()*0.5 - 0.25))
		d := base + jitter
		if d < 100*time.Millisecond {
			d = 100 * time.Millisecond
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}

	var data []byte
	var lastErr error
	backoff := initialBackoff
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		applyBrowserHeaders(req)
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http get: %w", err)
			sleepWithJitter(ctx, backoff)
			backoff *= 2
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				data, err = io.ReadAll(resp.Body)
				if err != nil {
					lastErr = fmt.Errorf("read body: %w", err)
					sleepWithJitter(ctx, backoff)
					backoff *= 2
					return
				}
				lastErr = nil
				return
			}
			// Retry on 429/5xx — Retry-After takes precedence over our backoff.
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, convErr := strconv.Atoi(ra); convErr == nil && secs > 0 {
						wait := time.Duration(secs) * time.Second
						if wait > maxBackoff {
							wait = maxBackoff
						}
						sleepWithJitter(ctx, wait)
					} else {
						sleepWithJitter(ctx, backoff)
						backoff *= 2
					}
				} else {
					sleepWithJitter(ctx, backoff)
					backoff *= 2
				}
				lastErr = fmt.Errorf("retryable status %d (attempt %d/%d)", resp.StatusCode, attempt+1, maxAttempts)
				return
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			if resp.StatusCode == http.StatusNotFound {
				// A caller can tell a page that does not exist from a failure.
				lastErr = fmt.Errorf("%w: unexpected status 404: %s", errNotFound, string(b))
			} else {
				lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
			}
		}()
		if lastErr == nil && len(data) > 0 {
			break
		}
		// Don't burn the rest of the budget on non-retryable errors. A body
		// read that broke mid-way (reset, truncated transfer) is transient.
		if lastErr != nil && !strings.HasPrefix(lastErr.Error(), "retryable status") && !strings.HasPrefix(lastErr.Error(), "http get") && !strings.HasPrefix(lastErr.Error(), "read body") {
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

// resumeBackoff is the wait before each new attempt of downloadResumable.
// Tests replace it with a function that does not wait.
var resumeBackoff = func(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// downloadResumable streams srcURL to destPath and continues a broken
// transfer at the byte where it stopped. The AN portal drops long transfers
// and sends 503 under load, and the amendments archive of a legislature is
// about 350 MB: a download that starts again from byte 0 on each failure does
// not end. The bytes go to destPath+".part", and each new attempt sends an
// HTTP Range request from the size of that file. destPath gets the file only
// when it holds the full body.
//
// A sidecar destPath+".part.meta" holds the validator of the file (ETag, else
// Last-Modified, else Content-Length) from the first response. A resume sends
// it as If-Range, so a file that changed on the server comes back whole (200)
// and the part file starts again. A part file that cannot be continued is
// removed and the download starts again at byte 0. The function stops after
// 7 attempts in sequence that add no byte, and keeps the part file for a
// later run. A status that a retry cannot fix stops the download and removes
// the part file.
func downloadResumable(ctx context.Context, srcURL, destPath string) error {
	const (
		maxStalled     = 7
		initialBackoff = 2 * time.Second
		maxBackoff     = 60 * time.Second
	)
	part := destPath + ".part"
	backoff := initialBackoff
	for stalled := 0; ; {
		offset := partSize(part)
		complete, retry, err := fetchFrom(ctx, srcURL, part, offset)
		if complete {
			if err := os.Rename(part, destPath); err != nil {
				return fmt.Errorf("rename %s: %w", part, err)
			}
			if err := os.Remove(part + partMetaSuffix); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", part+partMetaSuffix, err)
			}
			return nil
		}
		if !retry {
			if rmErr := removePart(part); rmErr != nil {
				return fmt.Errorf("%w; %v", err, rmErr)
			}
			return err
		}
		// A restart makes the part file smaller: that is not progress, so a
		// server that never lets the download continue stops at maxStalled.
		if partSize(part) > offset {
			stalled, backoff = 0, initialBackoff
		} else {
			stalled++
		}
		if stalled >= maxStalled {
			return fmt.Errorf("no progress after %d attempts: %w", maxStalled, err)
		}
		log.Warn().Err(err).Str("url", srcURL).Int64("bytes", partSize(part)).Int("stalled", stalled).
			Msg("frpolitics: download attempt failed; resuming")
		if err := resumeBackoff(ctx, min(backoff, maxBackoff)); err != nil {
			return err
		}
		backoff *= 2
	}
}

// partMetaSuffix is the suffix of the sidecar that holds the validator of a
// part file: "etag <value>", "last-modified <value>" or "length <bytes>".
const partMetaSuffix = ".meta"

// partSize is the size of the file at p, or 0 when the file does not exist.
func partSize(p string) int64 {
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return info.Size()
}

// removePart removes the part file and its sidecar.
func removePart(part string) error {
	for _, p := range []string{part, part + partMetaSuffix} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// responseValidator is the validator of the body in resp, for the sidecar.
// The value of ETag or Last-Modified is also the If-Range value of a resume.
// If-Range accepts only a strong ETag (RFC 9110, section 13.1.5): a server
// answers 200 to a weak one, so a weak ETag is not used.
func responseValidator(resp *http.Response) string {
	if v := resp.Header.Get("ETag"); v != "" && !strings.HasPrefix(v, "W/") {
		return "etag " + v
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		return "last-modified " + v
	}
	return fmt.Sprintf("length %d", resp.ContentLength)
}

// fetchFrom sends one GET for srcURL from byte offset and writes the body to
// part. complete is true when part holds the full body. retry is true when
// another attempt can succeed: a connection error, a broken body, 429, 5xx,
// or a part file that was removed because the server cannot continue it.
func fetchFrom(ctx context.Context, srcURL, part string, offset int64) (complete, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return false, false, fmt.Errorf("new request: %w", err)
	}
	applyBrowserHeaders(req)
	// A byte range applies to the raw body only, so ask for no content coding.
	req.Header.Set("Accept-Encoding", "identity")
	var validator string
	if offset > 0 {
		meta, err := os.ReadFile(part + partMetaSuffix)
		if err != nil {
			// A part file without its validator cannot be checked: start again.
			return false, true, restartPart(part, fmt.Errorf("read %s: %w", part+partMetaSuffix, err))
		}
		validator = string(meta)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if kind, value, _ := strings.Cut(validator, " "); kind != "length" {
			req.Header.Set("If-Range", value)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, true, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_CREATE
	start, total := int64(0), resp.ContentLength
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, new(int64), &total); err != nil || start != offset {
			return false, true, restartPart(part, fmt.Errorf("content-range %q does not continue at byte %d", cr, offset))
		}
		// Without ETag or Last-Modified, the server cannot check If-Range:
		// the size of the file is the only sign of a change.
		if validator != fmt.Sprintf("length %d", total) && strings.HasPrefix(validator, "length ") {
			return false, true, restartPart(part, fmt.Errorf("file size changed from %q to %d bytes", validator, total))
		}
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		// The server sends the full body (first attempt, or the file changed):
		// write the file again from byte 0.
		flags |= os.O_TRUNC
		if err := os.WriteFile(part+partMetaSuffix, []byte(responseValidator(resp)), 0o644); err != nil {
			return false, false, fmt.Errorf("write %s: %w", part+partMetaSuffix, err)
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// "bytes */<size>": the part file holds the full body when its size is <size>.
		var size int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes */%d", &size); err == nil && size == offset {
			return true, false, nil
		}
		return false, true, restartPart(part, fmt.Errorf("range from byte %d not satisfiable (%s)", offset, resp.Header.Get("Content-Range")))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true, fmt.Errorf("retryable status %d", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return false, false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return false, false, fmt.Errorf("open %s: %w", part, err)
	}
	n, copyErr := io.Copy(f, resp.Body)
	if err := f.Close(); err != nil {
		return false, false, fmt.Errorf("close %s: %w", part, err)
	}
	if copyErr != nil {
		return false, true, fmt.Errorf("read body: %w", copyErr)
	}
	if total >= 0 && start+n != total {
		return false, true, fmt.Errorf("body ends at byte %d of %d", start+n, total)
	}
	return true, false, nil
}

// restartPart removes the part file and its sidecar so that the next attempt
// starts at byte 0, and returns cause with the reason of the restart.
func restartPart(part string, cause error) error {
	if err := removePart(part); err != nil {
		return fmt.Errorf("%w; %v", cause, err)
	}
	return fmt.Errorf("%w; download starts again", cause)
}
