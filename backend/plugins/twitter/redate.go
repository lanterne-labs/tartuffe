package twitter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

// Redate moves every tweet fact to the tweet it comes from. Until 2026-10-30
// the facts of a batch of 20 tweets all carried the profile URL and the date
// of the batch's first tweet (the extractor read the batch as one page), so a
// fact's date was off by up to the span of its batch. The tweets' text was
// deleted at extraction, so the tweets without text are fetched again by id
// (RedateFetcher) and their text is stored back; then each fact source of
// the handle is matched to a tweet by its quote (findMatchingTweet). With
// Apply false nothing changes in fact_sources: the report says what would.
// The fetched text is stored in both modes: it is data we had, not a
// judgement.
//
// A source is matched among the tweets of its handle posted before it was
// stored: a later tweet cannot be where it comes from. When no tweet holds
// the quote whole, the tweet must hold redateMinOverlap of its words. The
// first run (2026-10-30) used the batch's 0.6 over every tweet of the handle
// and no date bound: 436 sources whose tweet was not stored yet went to
// another tweet that shared their common words, 400 of them to a tweet
// posted months after the fact was stored (a fact of June on a tweet of 29
// October, audit 2026-11-17). A source that sits on a tweet and matches none
// any more goes back to the profile URL, undated, until its tweet is stored
// (the backfill reads the older tweets) and a later run finds it.

// RedateStorer is the storage surface of Redate.
type RedateStorer interface {
	ListDocuments(ctx context.Context, sourceType string, afterID int64, limit int) ([]core.SourceDocument, error)
	SetDocumentContent(ctx context.Context, id int64, content string, publishedAt *time.Time) error
	ListFactSourcesByType(ctx context.Context, sourceType string, afterID int64, limit int) ([]storage.FactSourceRow, error)
	SetFactSourceURLAndDate(ctx context.Context, id int64, sourceURL string, publishedAt *time.Time) error
}

// RedateFetcher fetches tweets by id.
type RedateFetcher interface {
	FetchTweetsByIDs(ctx context.Context, ids []string) ([]Tweet, error)
}

// RedateOptions scopes one run.
type RedateOptions struct {
	// Apply writes the new URL and date of the matched fact sources.
	Apply bool
}

// RedateReport is the outcome of one run.
type RedateReport struct {
	Tweets        int // tweet documents read
	TweetsFetched int // tweets fetched again for their text
	TweetsMissing int // tweets the API no longer returns (deleted, protected)
	Sources       int // tweet fact sources read
	Matched       int // fact sources a tweet of their handle holds
	DateChanged   int // matched sources whose date differs from the stored one
	Reset         int // sources on a tweet that does not hold them: back to the profile URL, undated
	Unmatched     []RedateUnmatched
}

// RedateUnmatched is a fact source no tweet of its handle holds.
type RedateUnmatched struct {
	FactSourceID int64  `json:"fact_source_id"`
	FactID       int64  `json:"fact_id"`
	Handle       string `json:"handle"`
	Quote        string `json:"quote"`
}

// redateMinOverlap is the share of a quote's words a tweet must hold to be
// its source when no tweet of the handle holds the quote whole. Measured on
// 2026-11-17 over the 807 sources matched by their words: at 0.9 and above 5
// of 371 sat on a tweet posted after the source was stored, under it 400 of
// 436.
const redateMinOverlap = 0.9

// redatePage is how many rows one storage call reads.
const redatePage = 1000

// redateFetchSlice is how many tweets are fetched and stored at a time.
const redateFetchSlice = 500

// Redate runs the repair; see the package comment above.
func Redate(ctx context.Context, storer RedateStorer, fetcher RedateFetcher, opts RedateOptions) (RedateReport, error) {
	var rep RedateReport

	docs, err := allDocuments(ctx, storer, "twitter")
	if err != nil {
		return rep, err
	}
	rep.Tweets = len(docs)

	// Tweets without text are fetched again by id.
	byID := make(map[string]int, len(docs))
	var missingText []string
	for i, d := range docs {
		id, _ := d.Metadata["tweet_id"].(string)
		if id == "" {
			log.Warn().Int64("document_id", d.ID).Str("url", d.URL).Msg("twitter redate: document without a tweet id; skipped")
			continue
		}
		byID[id] = i
		if strings.TrimSpace(d.Content) == "" {
			missingText = append(missingText, id)
		}
	}
	// Fetched and stored in slices, so that a run cut short keeps what it
	// restored: the next run fetches only the tweets still without text.
	for start := 0; start < len(missingText); start += redateFetchSlice {
		end := min(start+redateFetchSlice, len(missingText))
		fetched, err := fetcher.FetchTweetsByIDs(ctx, missingText[start:end])
		if err != nil {
			return rep, fmt.Errorf("fetching tweets %d-%d of %d by id: %w", start, end, len(missingText), err)
		}
		rep.TweetsFetched += len(fetched)
		rep.TweetsMissing += end - start - len(fetched)
		for _, tw := range fetched {
			i, ok := byID[tw.ID]
			if !ok {
				continue
			}
			docs[i].Content = tw.Text
			if tw.CreatedAt != nil {
				docs[i].PublishedAt = tw.CreatedAt
			}
			if err := storer.SetDocumentContent(ctx, docs[i].ID, tw.Text, tw.CreatedAt); err != nil {
				return rep, err
			}
		}
		log.Info().Int("fetched", end).Int("of", len(missingText)).Int("missing", rep.TweetsMissing).Msg("twitter redate: tweets restored")
	}

	// The tweets of each handle, as the matcher reads them.
	byHandle := make(map[string][]Tweet)
	for _, d := range docs {
		if strings.TrimSpace(d.Content) == "" {
			continue
		}
		tw := tweetFromDocument(d)
		h := strings.ToLower(tw.AuthorHandle)
		byHandle[h] = append(byHandle[h], tw)
	}

	// Each fact source goes to the tweet of its handle that holds its quote.
	var afterID int64
	for {
		rows, err := storer.ListFactSourcesByType(ctx, "twitter", afterID, redatePage)
		if err != nil {
			return rep, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			afterID = r.ID
			rep.Sources++
			handle, _ := r.Metadata["twitter_handle"].(string)
			tw := matchTweet(backend.Fact{OriginalQuote: r.OriginalQuote}, postedBefore(byHandle[strings.ToLower(handle)], r.ScrapedAt), redateMinOverlap)
			if tw == nil {
				rep.Unmatched = append(rep.Unmatched, RedateUnmatched{FactSourceID: r.ID, FactID: r.FactID, Handle: handle, Quote: r.OriginalQuote})
				if handle == "" || !strings.Contains(r.SourceURL, "/status/") {
					continue
				}
				rep.Reset++
				if !opts.Apply {
					continue
				}
				if err := storer.SetFactSourceURLAndDate(ctx, r.ID, "https://x.com/"+strings.ToLower(handle), nil); err != nil {
					return rep, err
				}
				continue
			}
			rep.Matched++
			if !sameTime(r.PublishedAt, tw.CreatedAt) {
				rep.DateChanged++
			}
			if !opts.Apply || (r.SourceURL == tw.URL && sameTime(r.PublishedAt, tw.CreatedAt)) {
				continue
			}
			if err := storer.SetFactSourceURLAndDate(ctx, r.ID, tw.URL, tw.CreatedAt); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// postedBefore returns the tweets posted before the moment a source was
// stored (a tweet without a date is kept; a zero moment keeps them all).
func postedBefore(tweets []Tweet, stored time.Time) []Tweet {
	if stored.IsZero() {
		return tweets
	}
	out := make([]Tweet, 0, len(tweets))
	for _, tw := range tweets {
		if tw.CreatedAt == nil || !tw.CreatedAt.After(stored) {
			out = append(out, tw)
		}
	}
	return out
}

// allDocuments reads every document of the source type.
func allDocuments(ctx context.Context, storer RedateStorer, sourceType string) ([]core.SourceDocument, error) {
	var out []core.SourceDocument
	var afterID int64
	for {
		page, err := storer.ListDocuments(ctx, sourceType, afterID, redatePage)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return out, nil
		}
		out = append(out, page...)
		afterID = page[len(page)-1].ID
	}
}

// sameTime reports whether two optional times are the same instant.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
