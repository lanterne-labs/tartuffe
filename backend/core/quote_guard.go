package core

import (
	"errors"
	"strings"
)

// errMissingQuote marks a fact rejected because an LLM-extracted web source
// carried no verbatim quote. Like entity rejection, this is a per-fact skip (the
// claim is unverifiable), not a batch-aborting failure — see InsertFacts.
var errMissingQuote = errors.New("web-extracted fact missing original_quote")

// isQuoteMissing is the call-site predicate the fact-inserter uses to treat an
// empty-quote rejection as a skip rather than a hard failure.
func isQuoteMissing(err error) bool { return errors.Is(err, errMissingQuote) }

// isWebExtractedSource reports whether a fact's source_type is an LLM-extracted
// free-text web source, where an empty original_quote means the extractor
// produced a claim with no snippet behind it (a hallucination). Structured
// sources (e.g. "assemblee_law_page" vote records) always populate the quote
// from metadata and are intentionally excluded so they are never rejected here.
func isWebExtractedSource(sourceType string) bool {
	switch sourceType {
	case "news", "twitter":
		return true
	default:
		return false
	}
}

// quoteMissing reports whether a web-extracted fact lacks a usable source quote.
func quoteMissing(input StoreFactInput) bool {
	return isWebExtractedSource(input.SourceType) && strings.TrimSpace(input.OriginalQuote) == ""
}
