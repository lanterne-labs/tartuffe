package core

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"digital-mirror/backend/extractor"

	"github.com/rs/zerolog/log"
)

const (
	defaultConcurrentRequests = 5
	concurrentRequestsEnvVar  = "CONCURRENT_CRAWL_REQUESTS"

	// Rate limiting: delay between requests to avoid hitting API rate limits
	defaultRequestDelayMs = 500 // 0.5 second between requests
	requestDelayEnvVar    = "CRAWL_REQUEST_DELAY_MS"

	// Retry configuration for rate-limited requests
	maxRetries       = 3
	initialBackoff   = 2 * time.Second
	backoffMultipler = 2
)

// BatchExtractor wraps a ContentExtractor to provide concurrent batch extraction
// with configurable concurrency limits. This is useful for APIs like Firecrawl
// that have rate limits on concurrent requests.
type BatchExtractor struct {
	inner         extractor.ContentExtractor
	maxConcurrent int
	requestDelay  time.Duration // Delay between requests to respect rate limits
}

// BatchResult represents the result of extracting a single URL in a batch.
type BatchResult struct {
	Index   int                         // Original index in the input slice (for ordering)
	URL     string                      // The URL that was extracted
	Content *extractor.ExtractedContent // Extracted content, nil if error
	Error   error                       // Error if extraction failed
}

// job represents a work item for the worker pool.
type job struct {
	Index int
	URL   string
}

// NewBatchExtractor creates a new BatchExtractor wrapping the given ContentExtractor.
// maxConcurrent controls how many concurrent requests are allowed.
// requestDelay is the minimum time between requests (to respect rate limits).
func NewBatchExtractor(inner extractor.ContentExtractor, maxConcurrent int, requestDelay time.Duration) *BatchExtractor {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &BatchExtractor{
		inner:         inner,
		maxConcurrent: maxConcurrent,
		requestDelay:  requestDelay,
	}
}

// NewBatchExtractorFromEnv creates a BatchExtractor with settings loaded from environment:
// - CONCURRENT_CRAWL_REQUESTS: max concurrent requests (default: 2)
// - CRAWL_REQUEST_DELAY_MS: delay between requests in milliseconds (default: 1000)
func NewBatchExtractorFromEnv(inner extractor.ContentExtractor) *BatchExtractor {
	maxConcurrent := defaultConcurrentRequests
	requestDelayMs := defaultRequestDelayMs

	if env := os.Getenv(concurrentRequestsEnvVar); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			maxConcurrent = n
		} else {
			log.Warn().
				Str("value", env).
				Int("default", defaultConcurrentRequests).
				Msg("Invalid CONCURRENT_CRAWL_REQUESTS value, using default")
		}
	}

	if env := os.Getenv(requestDelayEnvVar); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n >= 0 {
			requestDelayMs = n
		} else {
			log.Warn().
				Str("value", env).
				Int("default", defaultRequestDelayMs).
				Msg("Invalid CRAWL_REQUEST_DELAY_MS value, using default")
		}
	}

	requestDelay := time.Duration(requestDelayMs) * time.Millisecond

	log.Info().
		Int("max_concurrent", maxConcurrent).
		Dur("request_delay", requestDelay).
		Msg("Initialized batch extractor with rate limiting")

	return NewBatchExtractor(inner, maxConcurrent, requestDelay)
}

// ExtractContent implements the ContentExtractor interface for single URL extraction.
func (b *BatchExtractor) ExtractContent(url string) (*extractor.ExtractedContent, error) {
	return b.inner.ExtractContent(url)
}

// ExtractBatch extracts content from multiple URLs concurrently using a worker pool.
// It respects the maxConcurrent limit to avoid overwhelming rate-limited APIs.
// Results are returned in the same order as the input URLs.
func (b *BatchExtractor) ExtractBatch(ctx context.Context, urls []string) []BatchResult {
	if len(urls) == 0 {
		return []BatchResult{}
	}

	log.Info().
		Int("url_count", len(urls)).
		Int("max_concurrent", b.maxConcurrent).
		Msg("Starting batch extraction")

	jobs := make(chan job)
	results := make(chan BatchResult, len(urls))

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < b.maxConcurrent; i++ {
		wg.Add(1)
		go b.worker(ctx, &wg, jobs, results)
	}

	// Send jobs in a separate goroutine
	go func() {
		for i, url := range urls {
			select {
			case jobs <- job{Index: i, URL: url}:
			case <-ctx.Done():
				break
			}
		}
		close(jobs)
	}()

	// Wait for all workers to finish, then close results channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	output := make([]BatchResult, len(urls))
	for r := range results {
		output[r.Index] = r
	}

	// Count successes and failures
	successCount := 0
	errorCount := 0
	for _, r := range output {
		if r.Error == nil && r.Content != nil {
			successCount++
		} else {
			errorCount++
		}
	}

	log.Info().
		Int("success", successCount).
		Int("errors", errorCount).
		Int("total", len(urls)).
		Msg("Batch extraction completed")

	return output
}

// worker is a goroutine that processes jobs from the jobs channel.
func (b *BatchExtractor) worker(ctx context.Context, wg *sync.WaitGroup, jobs <-chan job, results chan<- BatchResult) {
	defer wg.Done()

	for j := range jobs {
		// Check context cancellation
		select {
		case <-ctx.Done():
			results <- BatchResult{
				Index: j.Index,
				URL:   j.URL,
				Error: ctx.Err(),
			}
			continue
		default:
		}

		log.Debug().
			Int("index", j.Index).
			Str("url", j.URL).
			Msg("Worker extracting URL")

		// Extract with retry for rate-limited requests
		content, err := b.extractWithRetry(ctx, j.URL)

		results <- BatchResult{
			Index:   j.Index,
			URL:     j.URL,
			Content: content,
			Error:   err,
		}

		if err != nil {
			log.Debug().
				Err(err).
				Int("index", j.Index).
				Str("url", j.URL).
				Msg("Worker extraction failed")
		} else {
			log.Debug().
				Int("index", j.Index).
				Str("url", j.URL).
				Msg("Worker extraction succeeded")
		}

		// Rate limiting: delay before next request
		if b.requestDelay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(b.requestDelay):
			}
		}
	}
}

// extractWithRetry attempts extraction with exponential backoff on 429 errors.
func (b *BatchExtractor) extractWithRetry(ctx context.Context, url string) (*extractor.ExtractedContent, error) {
	var lastErr error
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Info().
				Int("attempt", attempt+1).
				Dur("backoff", backoff).
				Str("url", url).
				Msg("Retrying after rate limit")

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}

			backoff *= backoffMultipler
		}

		content, err := b.inner.ExtractContent(url)
		if err == nil {
			return content, nil
		}

		lastErr = err

		// Check if it's a rate limit error (429)
		if !isRateLimitError(err) {
			return nil, err
		}

		log.Warn().
			Int("attempt", attempt+1).
			Str("url", url).
			Msg("Rate limited (429), will retry")
	}

	log.Error().
		Err(lastErr).
		Str("url", url).
		Int("max_retries", maxRetries).
		Msg("Extraction failed after all retries")

	return nil, lastErr
}

// isRateLimitError checks if the error is a 429 rate limit error.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "status 429") || strings.Contains(errStr, "Too Many Requests")
}

// MaxConcurrent returns the current concurrency limit.
func (b *BatchExtractor) MaxConcurrent() int {
	return b.maxConcurrent
}
