package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	twitterAPIIOBaseURL   = "https://api.twitterapi.io/twitter/tweet/advanced_search"
	twitterAPIIOLookupURL = "https://api.twitterapi.io/twitter/tweets"
	// lookupIDsPerCall is how many tweet ids one lookup request takes: the
	// API refuses more ("max 50 tweet_ids per request").
	lookupIDsPerCall      = 50
	httpTimeout           = 30 * time.Second
	defaultQPSDelayMillis = 5000 // 5 seconds for free tier (1 req per 5s)
)

// TwitterAPIIOFetcher fetches tweets from twitterapi.io.
type TwitterAPIIOFetcher struct {
	apiKey   string
	client   *http.Client
	qpsDelay time.Duration
}

// NewTwitterAPIIOFetcherFromEnv creates a fetcher using TWITTER_API_IO_KEY env var.
// TWITTER_API_IO_QPS_DELAY_MS controls the delay between API calls in milliseconds.
// Defaults to 5000ms (free tier limit: 1 request per 5 seconds).
func NewTwitterAPIIOFetcherFromEnv() (*TwitterAPIIOFetcher, error) {
	apiKey := os.Getenv("TWITTER_API_IO_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("TWITTER_API_IO_KEY environment variable not set")
	}

	qpsDelay := time.Duration(defaultQPSDelayMillis) * time.Millisecond
	if delayStr := os.Getenv("TWITTER_API_IO_QPS_DELAY_MS"); delayStr != "" {
		if ms, err := strconv.Atoi(delayStr); err == nil && ms >= 0 {
			qpsDelay = time.Duration(ms) * time.Millisecond
			log.Info().Int("delay_ms", ms).Msg("twitter: using custom QPS delay")
		} else {
			log.Warn().Str("value", delayStr).Msg("twitter: invalid TWITTER_API_IO_QPS_DELAY_MS, using default")
		}
	}

	return &TwitterAPIIOFetcher{
		apiKey:   apiKey,
		client:   &http.Client{Timeout: httpTimeout},
		qpsDelay: qpsDelay,
	}, nil
}

// twitterAPIIOResponse represents the API response from twitterapi.io.
type twitterAPIIOResponse struct {
	Tweets      []twitterAPIIOTweet `json:"tweets"`
	HasNextPage bool                `json:"has_next_page"`
	NextCursor  string              `json:"next_cursor"`
}

// twitterAPIIOTweet represents a single tweet from twitterapi.io.
type twitterAPIIOTweet struct {
	ID             string             `json:"id"`
	Text           string             `json:"text"`
	URL            string             `json:"url"`
	CreatedAt      string             `json:"createdAt"`
	Lang           string             `json:"lang"`
	Author         twitterAPIIOAuthor `json:"author"`
	RetweetedTweet *json.RawMessage   `json:"retweeted_tweet"`
}

// twitterAPIIOAuthor represents the tweet author from twitterapi.io.
type twitterAPIIOAuthor struct {
	UserName string `json:"userName"`
	Name     string `json:"name"`
}

// FetchTweets fetches tweets matching the query from twitterapi.io.
func (f *TwitterAPIIOFetcher) FetchTweets(ctx context.Context, query string, opts FetchOptions) ([]Tweet, error) {
	maxTweets := opts.MaxTweets
	if maxTweets <= 0 {
		maxTweets = 50
	}

	// Build query with date constraints
	fullQuery := query
	if opts.DateFrom != nil {
		fullQuery += fmt.Sprintf(" since:%s", opts.DateFrom.Format("2006-01-02_15:04:05_UTC"))
	}
	if opts.DateTo != nil {
		fullQuery += fmt.Sprintf(" until:%s", opts.DateTo.Format("2006-01-02_15:04:05_UTC"))
	}

	var allTweets []Tweet
	cursor := ""

	for len(allTweets) < maxTweets {
		select {
		case <-ctx.Done():
			return allTweets, ctx.Err()
		default:
		}

		// Rate limit: wait before each API call to respect QPS limits
		if f.qpsDelay > 0 {
			log.Debug().Dur("delay", f.qpsDelay).Msg("twitter: rate limit delay")
			select {
			case <-time.After(f.qpsDelay):
			case <-ctx.Done():
				return allTweets, ctx.Err()
			}
		}

		resp, err := f.fetchPage(ctx, fullQuery, cursor)
		if err != nil {
			return allTweets, fmt.Errorf("failed to fetch tweets page: %w", err)
		}

		for _, t := range resp.Tweets {
			if len(allTweets) >= maxTweets {
				break
			}

			tweet := mapTweet(t)
			allTweets = append(allTweets, tweet)
		}

		if !resp.HasNextPage || resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor

		log.Debug().
			Int("fetched_so_far", len(allTweets)).
			Int("max", maxTweets).
			Msg("twitter: fetching next page")
	}

	log.Info().
		Str("query", fullQuery).
		Int("tweets_fetched", len(allTweets)).
		Msg("twitter: fetch completed")

	return allTweets, nil
}

// fetchPage fetches a single page of results from the API.
func (f *TwitterAPIIOFetcher) fetchPage(ctx context.Context, query, cursor string) (*twitterAPIIOResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("queryType", "Latest")
	if cursor != "" {
		params.Set("cursor", cursor)
	}

	reqURL := twitterAPIIOBaseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", f.apiKey)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp twitterAPIIOResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &apiResp, nil
}

// mapTweet converts an API tweet to our Tweet type.
func mapTweet(t twitterAPIIOTweet) Tweet {
	tweet := Tweet{
		ID:           t.ID,
		Text:         t.Text,
		AuthorHandle: t.Author.UserName,
		AuthorName:   t.Author.Name,
		URL:          t.URL,
		Lang:         t.Lang,
		IsRetweet:    t.RetweetedTweet != nil && string(*t.RetweetedTweet) != "null",
	}

	if t.CreatedAt != "" {
		// Try multiple formats that Twitter uses
		for _, layout := range []string{
			time.RFC3339,
			"Mon Jan 02 15:04:05 -0700 2006",
			"2006-01-02T15:04:05.000Z",
		} {
			if parsed, err := time.Parse(layout, t.CreatedAt); err == nil {
				tweet.CreatedAt = &parsed
				break
			}
		}
	}

	return tweet
}

// FetchTweetsByIDs returns the tweets with the given ids, in calls of
// lookupIDsPerCall ids. A deleted or protected tweet is simply absent from
// the result.
func (f *TwitterAPIIOFetcher) FetchTweetsByIDs(ctx context.Context, ids []string) ([]Tweet, error) {
	var out []Tweet
	for start := 0; start < len(ids); start += lookupIDsPerCall {
		end := min(start+lookupIDsPerCall, len(ids))
		if start > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(f.qpsDelay):
			}
		}
		tweets, err := f.lookup(ctx, ids[start:end])
		if err != nil {
			return out, err
		}
		out = append(out, tweets...)
	}
	return out, nil
}

// lookup is one lookup call, bounded by httpTimeout through the context (a
// dead HTTP/2 connection otherwise hangs the call for good: the dry run of
// 2026-10-30 sat 25 minutes on one request) and tried twice.
func (f *TwitterAPIIOFetcher) lookup(ctx context.Context, ids []string) ([]Tweet, error) {
	params := url.Values{}
	params.Set("tweet_ids", strings.Join(ids, ","))
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, httpTimeout)
		tweets, err := f.lookupOnce(cctx, params)
		cancel()
		if err == nil {
			return tweets, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		log.Warn().Err(err).Int("attempt", attempt).Int("ids", len(ids)).Msg("twitter: tweet lookup failed")
	}
	return nil, lastErr
}

func (f *TwitterAPIIOFetcher) lookupOnce(ctx context.Context, params url.Values) ([]Tweet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, twitterAPIIOLookupURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tweet lookup request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tweet lookup returned status %d: %s", resp.StatusCode, string(body))
	}
	var apiResp twitterAPIIOResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode tweet lookup response: %w", err)
	}
	out := make([]Tweet, 0, len(apiResp.Tweets))
	for _, t := range apiResp.Tweets {
		out = append(out, mapTweet(t))
	}
	return out, nil
}
