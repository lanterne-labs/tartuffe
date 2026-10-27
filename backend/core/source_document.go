package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"errors"
	"fmt"
	"sort"
	"sync"
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
// A failure because the LLM was unreachable (llm.ErrUnavailable), or because
// the run was cancelled, leaves the document as it was: the attempts count
// only failures of the document.
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
	// every person, a zero publishedSince any publication date.
	ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, publishedSince time.Time, limit int) ([]SourceDocument, error)
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
	// Parallel runs the batches as many at once as the endpoint serves
	// (llm.ForEach) instead of one after the other. extract must then be
	// safe to call at once. The facts of one subject are still inserted one
	// at a time (the subject lock of the inserter): only the extraction and
	// the insertion of different subjects overlap. The streak of
	// MaxConsecutiveFailures counts in the order the batches end.
	Parallel bool
	// Order, when set, sorts the unfinished documents before processing
	// (default: oldest fetch first).
	Order func(a, b SourceDocument) bool
	// Limit caps how many unfinished documents are loaded (0 = 2000).
	Limit int
	// PublishedSince, when set, leaves out documents published before it
	// (and undated ones): a feed bounded to recent content must not walk the
	// backlog of older documents, which is a deadline-bound backfill's job.
	PublishedSince time.Time
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

// FailsDocument reports whether a failure to embed or store one fact of a
// document must fail the whole document: an unreachable LLM
// (llm.ErrUnavailable) or a cancelled run. Any other failure is the fact's own
// (a deterministic stance failure, an unparseable judge answer, a 4xx) and
// skips only that fact; errors do not tell a database outage apart cleanly,
// but one also fails marking the document extracted, which leaves it pending.
func FailsDocument(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil)
}

// ProcessDocuments drives every unfinished document of sourceType (and
// personID, when non-zero) through extract, recording each document's
// transition as soon as its batch finishes. A cancelled ctx stops it before
// the next batch, or under one, with an error wrapping ctx.Err().
func ProcessDocuments(ctx context.Context, storer DocumentStorer, sourceType string, personID int64, extract ExtractBatchFunc, opts ProcessOptions) (ProcessStats, error) {
	var st ProcessStats
	limit := opts.Limit
	if limit <= 0 {
		limit = 2000
	}
	docs, err := storer.ListUnfinishedDocuments(ctx, sourceType, personID, opts.PublishedSince, limit)
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

	var batches [][]SourceDocument
	for i := 0; i < len(docs); i += batchSize {
		batches = append(batches, docs[i:min(i+batchSize, len(docs))])
	}
	var (
		mu          sync.Mutex // st, consecutive, lastErr, started
		consecutive int
		lastErr     error
		started     int // documents of the batches started, for MaxDocs
	)
	// errEnough stops the run without an error: the deadline or MaxDocs.
	errEnough := errors.New("enough documents")
	run := func(ctx context.Context, batch []SourceDocument) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s documents stopped: %w", sourceType, err)
		}
		mu.Lock()
		if !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			st.DeadlineHit = true
			mu.Unlock()
			return errEnough
		}
		if opts.MaxDocs > 0 && started >= opts.MaxDocs {
			mu.Unlock()
			return errEnough
		}
		started += len(batch)
		mu.Unlock()

		facts, err := extract(ctx, batch)
		if err != nil && ctx.Err() != nil {
			// The run was cancelled under the batch: not the documents' fault,
			// they stay as they were for the next run.
			return fmt.Errorf("%s documents stopped: %w", sourceType, ctx.Err())
		}
		mu.Lock()
		defer mu.Unlock()
		st.Attempted += len(batch)
		if err != nil {
			lastErr = err
			consecutive++
			st.Failed += len(batch)
			metrics.AddDecision(metrics.StageDocumentExtract, metrics.OutcomeDocFailed, int64(len(batch)))
			// An unreachable LLM is not the document's fault: it keeps its
			// attempts, so a night-long outage cannot use up its retries.
			unavailable := errors.Is(err, llm.ErrUnavailable)
			for _, d := range batch {
				if unavailable {
					log.Warn().Err(err).Str("source", sourceType).Str("url", d.URL).
						Msg("document extraction failed: LLM unavailable; attempt not counted")
					continue
				}
				log.Warn().Err(err).Str("source", sourceType).Str("url", d.URL).Int("attempt", d.Attempts+1).
					Msg("document extraction failed; left unfinished for the next run")
				if mErr := storer.MarkDocumentFailed(ctx, d.ID, err); mErr != nil {
					return mErr
				}
			}
			if opts.MaxConsecutiveFailures > 0 && consecutive >= opts.MaxConsecutiveFailures {
				return fmt.Errorf("%w: %d consecutive failures (last: %w)", ErrOutage, consecutive, err)
			}
			return nil
		}
		consecutive = 0
		for _, d := range batch {
			n := facts[d.ID]
			st.Extracted++
			st.Facts += n
			if n > 0 {
				metrics.RecordDecision(metrics.StageDocumentExtract, metrics.OutcomeDocExtracted)
			} else {
				metrics.RecordDecision(metrics.StageDocumentExtract, metrics.OutcomeDocNoFacts)
			}
			if mErr := storer.MarkDocumentExtracted(ctx, d.ID, n); mErr != nil {
				return mErr
			}
		}
		return nil
	}
	if opts.Parallel {
		err = llm.ForEach(ctx, batches, run)
	} else {
		for _, batch := range batches {
			if err = run(ctx, batch); err != nil {
				break
			}
		}
	}
	if err != nil && !errors.Is(err, errEnough) {
		return st, err
	}
	if st.Attempted > 0 && st.Extracted == 0 && st.Failed > 0 {
		return st, fmt.Errorf("%w: every attempted document failed (%d, last: %w)", ErrOutage, st.Failed, lastErr)
	}
	return st, nil
}
