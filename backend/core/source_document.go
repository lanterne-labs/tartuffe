package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// SourceDocument is one unit of raw material a plugin fetched (a scraped
// article, a tweet, a speaker's interventions in a séance) and the state of
// its extraction into facts. Every plugin's documents live in one table and
// move through one state machine:
//
//	fetched ──extract ok──▶ extracted   (facts_count recorded, content deleted;
//	   │                                 the row stays: the URL is never fetched again)
//	   └──extract err─▶ failed ──retry─▶ extracted | failed  (until MaxExtractionAttempts)
//
// The fetch step saves the document the moment the fetch succeeds, before
// any extraction; ProcessDocuments drives every unfinished document to
// extracted, whichever run fetched it.
type SourceDocument struct {
	ID          int64
	SourceType  string // "news", "twitter", "debats"
	URL         string // unique per SourceType
	PersonID    int64  // the target the document was collected for (0 = none)
	Title       string
	Content     string // raw material; empty once extracted
	PublishedAt *time.Time
	Metadata    map[string]any
	FetchedAt   time.Time
	State       string
	Attempts    int
	FactsCount  int
}

// Document states.
const (
	DocFetched   = "fetched"
	DocExtracted = "extracted"
	DocFailed    = "failed"
)

// MaxExtractionAttempts bounds how many runs retry a failed document.
const MaxExtractionAttempts = 3

// DocumentStorer is the persistence of the document state machine.
// *storage.PostgresStorer implements it.
type DocumentStorer interface {
	// SaveFetchedDocuments upserts documents by (source_type, url) into the
	// fetched state and returns their ids by url: a refetch replaces the
	// content and restarts the attempts.
	SaveFetchedDocuments(ctx context.Context, docs []SourceDocument) (map[string]int64, error)
	// KnownDocumentURLs returns which of the urls already have a document of
	// the source type, in any state.
	KnownDocumentURLs(ctx context.Context, sourceType string, urls []string) (map[string]bool, error)
	// ListUnfinishedDocuments returns the documents still to extract (fetched,
	// or failed with attempts left), oldest fetch first; personID 0 means
	// every person.
	ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, limit int) ([]SourceDocument, error)
	// MarkDocumentExtracted records a successful extraction and deletes the
	// raw content.
	MarkDocumentExtracted(ctx context.Context, id int64, factsCount int) error
	// MarkDocumentFailed records a failed attempt.
	MarkDocumentFailed(ctx context.Context, id int64, extractErr error) error
}

// ExtractBatchFunc turns a batch of documents into stored facts and returns
// how many facts each document produced. An error fails the whole batch.
type ExtractBatchFunc func(ctx context.Context, docs []SourceDocument) (factsByDocID map[int64]int, err error)

// ProcessOptions tunes ProcessDocuments.
type ProcessOptions struct {
	// BatchSize is how many documents one extract call receives (0 = 1).
	BatchSize int
	// Deadline, when set, stops starting batches after it.
	Deadline time.Time
	// MaxDocs, when > 0, caps how many documents the run attempts.
	MaxDocs int
	// MaxConsecutiveFailures, when > 0, aborts the run after that many
	// failed batches in a row: an outage, not bad documents.
	MaxConsecutiveFailures int
	// Order, when set, sorts the unfinished documents before processing
	// (default: oldest fetch first).
	Order func(a, b SourceDocument) bool
	// Limit caps how many unfinished documents are loaded (0 = 2000).
	Limit int
}

// ProcessStats are the counters of one ProcessDocuments run.
type ProcessStats struct {
	Pending     int // unfinished documents found before the run
	Attempted   int
	Extracted   int
	Failed      int
	Facts       int
	DeadlineHit bool
}

// ErrOutage is returned when a run aborts on a streak of failures or when
// every attempted document failed: the extraction dependency is down, and
// the documents stay unfinished for the next run.
var ErrOutage = errors.New("document extraction outage")

// ProcessDocuments drives every unfinished document of sourceType (and
// personID, when non-zero) through extract, recording each document's
// transition as soon as its batch finishes.
func ProcessDocuments(ctx context.Context, storer DocumentStorer, sourceType string, personID int64, extract ExtractBatchFunc, opts ProcessOptions) (ProcessStats, error) {
	var st ProcessStats
	limit := opts.Limit
	if limit <= 0 {
		limit = 2000
	}
	docs, err := storer.ListUnfinishedDocuments(ctx, sourceType, personID, limit)
	if err != nil {
		return st, fmt.Errorf("listing unfinished %s documents: %w", sourceType, err)
	}
	st.Pending = len(docs)
	if len(docs) == 0 {
		return st, nil
	}
	if opts.Order != nil {
		sort.SliceStable(docs, func(i, j int) bool { return opts.Order(docs[i], docs[j]) })
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}

	consecutive := 0
	var lastErr error
	for i := 0; i < len(docs); i += batchSize {
		if ctx.Err() != nil {
			break
		}
		if !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			st.DeadlineHit = true
			break
		}
		if opts.MaxDocs > 0 && st.Attempted >= opts.MaxDocs {
			break
		}
		end := i + batchSize
		if end > len(docs) {
			end = len(docs)
		}
		batch := docs[i:end]
		st.Attempted += len(batch)

		facts, err := extract(ctx, batch)
		if err != nil {
			lastErr = err
			consecutive++
			st.Failed += len(batch)
			for _, d := range batch {
				log.Warn().Err(err).Str("source", sourceType).Str("url", d.URL).Int("attempt", d.Attempts+1).
					Msg("document extraction failed; left unfinished for the next run")
				if mErr := storer.MarkDocumentFailed(ctx, d.ID, err); mErr != nil {
					return st, mErr
				}
			}
			if opts.MaxConsecutiveFailures > 0 && consecutive >= opts.MaxConsecutiveFailures {
				return st, fmt.Errorf("%w: %d consecutive failures (last: %v)", ErrOutage, consecutive, err)
			}
			continue
		}
		consecutive = 0
		for _, d := range batch {
			n := facts[d.ID]
			st.Extracted++
			st.Facts += n
			if mErr := storer.MarkDocumentExtracted(ctx, d.ID, n); mErr != nil {
				return st, mErr
			}
		}
	}
	if st.Attempted > 0 && st.Extracted == 0 && st.Failed > 0 {
		return st, fmt.Errorf("%w: every attempted document failed (%d, last: %v)", ErrOutage, st.Failed, lastErr)
	}
	return st, nil
}
