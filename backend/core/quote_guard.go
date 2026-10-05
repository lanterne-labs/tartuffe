package core

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

// errMissingQuote marks a fact rejected because an LLM-extracted web source
// carried no verbatim quote. Like entity rejection, this is a per-fact skip (the
// claim is unverifiable), not a batch-aborting failure — see InsertFacts.
var errMissingQuote = errors.New("web-extracted fact missing original_quote")

// isQuoteMissing is the call-site predicate the fact-inserter uses to treat an
// empty-quote rejection as a skip rather than a hard failure.
func isQuoteMissing(err error) bool { return errors.Is(err, errMissingQuote) }

// quoteRequiredSourceTypes holds the source types whose facts are LLM-extracted
// from free text, where an empty original_quote means the extractor produced a
// claim with no snippet behind it (a hallucination). Each plugin registers its
// own types at init. Structured sources (e.g. "assemblee_law_page" vote
// records) always populate the quote from metadata and stay unregistered so
// they are never rejected here.
var (
	quoteRequiredMu          sync.RWMutex
	quoteRequiredSourceTypes = map[string]bool{}
)

// RequireQuoteForSourceType marks sourceType as LLM-extracted free text: facts
// of that type are rejected at insertion when original_quote is empty.
func RequireQuoteForSourceType(sourceType string) {
	if sourceType == "" {
		panic("core.RequireQuoteForSourceType: empty source type")
	}
	quoteRequiredMu.Lock()
	defer quoteRequiredMu.Unlock()
	quoteRequiredSourceTypes[sourceType] = true
}

// isWebExtractedSource reports whether a fact's source_type was registered
// with RequireQuoteForSourceType.
func isWebExtractedSource(sourceType string) bool {
	quoteRequiredMu.RLock()
	defer quoteRequiredMu.RUnlock()
	return quoteRequiredSourceTypes[sourceType]
}

// quoteMissing reports whether a web-extracted fact lacks a usable source quote.
func quoteMissing(input StoreFactInput) bool {
	return isWebExtractedSource(input.SourceType) && strings.TrimSpace(input.OriginalQuote) == ""
}

// freeTextSourceTypes returns the registered free-text source types, sorted.
// Only facts from these sources can be grouped: a structured record (a vote)
// is identified by its record, so two of them are never one claim.
func freeTextSourceTypes() []string {
	quoteRequiredMu.RLock()
	defer quoteRequiredMu.RUnlock()
	types := make([]string, 0, len(quoteRequiredSourceTypes))
	for t := range quoteRequiredSourceTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
