package core

import (
	"context"
	"errors"
	"time"
)

// errPageFurniture marks a web fact whose quote the person already has from
// ANOTHER page published at another date: the sentence is the site's ticker,
// "à la une" block or related-links list, not the article's (review of
// 2026-10-27, mirror-d3a: 68 such headlines made 457 facts, dated by pages
// of 2011 to 2026 for news of October 2026). Like a missing quote, it is a
// per-fact skip.
var errPageFurniture = errors.New("web-extracted quote already stored from a page of another date")

func isPageFurniture(err error) bool { return errors.Is(err, errPageFurniture) }

// pageFurnitureGap is how far apart two pages' dates must be for a shared
// quote to be furniture: the same article republished or updated within a
// few days is one occasion and keeps its own fact (the grouping joins them).
const pageFurnitureGap = 3 * 24 * time.Hour

// QuoteElsewhereStorer reports whether a person already has a fact source
// with the same quote on another URL whose publication date is more than
// gap away from publishedAt (a source without a date counts as another
// date). Implemented by storage.PostgresStorer; a Storer without it skips
// the check.
type QuoteElsewhereStorer interface {
	QuoteOnPageOfOtherDate(ctx context.Context, personID int64, quote, sourceURL string, publishedAt *time.Time, gap time.Duration) (bool, error)
}

// checkPageFurniture returns errPageFurniture when the quote of a web fact
// is already stored for the person from a page of another date.
func (fi *FactInserter) checkPageFurniture(ctx context.Context, personID int64, input StoreFactInput) error {
	if !isWebExtractedSource(input.SourceType) {
		return nil
	}
	qs, ok := fi.storer.(QuoteElsewhereStorer)
	if !ok {
		return nil
	}
	seen, err := qs.QuoteOnPageOfOtherDate(ctx, personID, input.OriginalQuote, input.SourceURL, input.PublishedAt, pageFurnitureGap)
	if err != nil {
		return err
	}
	if seen {
		return errPageFurniture
	}
	return nil
}
