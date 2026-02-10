package twitter

import (
	"context"
	"time"
)

// Tweet represents a single tweet fetched from the API.
type Tweet struct {
	ID           string
	Text         string
	AuthorHandle string
	AuthorName   string
	URL          string
	Lang         string
	CreatedAt    *time.Time
	IsRetweet    bool
}

// TweetFetcher fetches tweets from an external API.
type TweetFetcher interface {
	FetchTweets(ctx context.Context, query string, opts FetchOptions) ([]Tweet, error)
}

// FetchOptions configures tweet fetching.
type FetchOptions struct {
	MaxTweets int
	DateFrom  *time.Time
	DateTo    *time.Time
}

// Options configures a twitter collection run.
type Options struct {
	// MaxTweets is the maximum number of tweets to fetch per handle (default 50).
	MaxTweets int
	// RevisitHours controls how often a URL can be revisited (0 = always revisit).
	RevisitHours int
	// DryRun shows what would be done without actually fetching or storing anything.
	DryRun bool
	// Force bypasses both API-level and tweet-level dedup.
	Force bool
	// BatchSize is the number of tweets per LLM call (default 20).
	BatchSize int
	// WindowDays breaks the date range into windows of N days.
	WindowDays int
	// DateFrom is the explicit start of the date range.
	DateFrom *time.Time
	// DateTo is the explicit end of the date range.
	DateTo *time.Time
}

// Result contains the outcome of a twitter collection for a single handle.
type Result struct {
	Handle                 string
	PersonName             string
	TweetsFetched          int
	TweetsProcessed        int
	TweetsProcessedNoFacts int
	FactsExtracted         int
	SkippedAlreadyProcessed int
	SkippedRetweets        int
	SkippedAlreadyFetchedRange bool
	Errors                 []error
}

// aggregateResult adds the counters from src into dst.
func aggregateResult(dst *Result, src *Result) {
	dst.TweetsFetched += src.TweetsFetched
	dst.TweetsProcessed += src.TweetsProcessed
	dst.TweetsProcessedNoFacts += src.TweetsProcessedNoFacts
	dst.FactsExtracted += src.FactsExtracted
	dst.SkippedAlreadyProcessed += src.SkippedAlreadyProcessed
	dst.SkippedRetweets += src.SkippedRetweets
	dst.Errors = append(dst.Errors, src.Errors...)
}
