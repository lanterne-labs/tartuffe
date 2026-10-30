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
	Unmatched     []RedateUnmatched
}

// RedateUnmatched is a fact source no tweet of its handle holds.
type RedateUnmatched struct {
	FactSourceID int64  `json:"fact_source_id"`
	FactID       int64  `json:"fact_id"`
	Handle       string `json:"handle"`
	Quote        string `json:"quote"`
}

// redatePage is how many rows one storage call reads.
const redatePage = 1000

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
	if len(missingText) > 0 {
		fetched, err := fetcher.FetchTweetsByIDs(ctx, missingText)
		if err != nil {
			return rep, fmt.Errorf("fetching %d tweets by id: %w", len(missingText), err)
		}
		rep.TweetsFetched = len(fetched)
		rep.TweetsMissing = len(missingText) - len(fetched)
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
			tw := findMatchingTweet(backend.Fact{OriginalQuote: r.OriginalQuote}, byHandle[strings.ToLower(handle)])
			if tw == nil {
				rep.Unmatched = append(rep.Unmatched, RedateUnmatched{FactSourceID: r.ID, FactID: r.FactID, Handle: handle, Quote: r.OriginalQuote})
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
