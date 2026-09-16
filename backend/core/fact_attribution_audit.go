package core

import (
	"context"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/util"
	"strings"
)

// Decisions of the source attribution audit.
const (
	AttributionKeep              = "KEEP"
	AttributionDeleteSource      = "DELETE_SOURCE"
	AttributionDeleteFactCascade = "DELETE_FACT_CASCADE"
	AttributionSkipScrapeFailed  = "SKIP_SCRAPE_FAILED"
)

// FactSourceForAudit is one fact_sources row to verify.
type FactSourceForAudit struct {
	ID            int64
	FactID        int64
	SourceURL     string
	OriginalQuote string
}

// AttributionDecision is one row of the attribution audit log.
type AttributionDecision struct {
	Timestamp    string `json:"ts"`
	FactID       int64  `json:"fact_id"`
	FactSourceID int64  `json:"fact_source_id"`
	SourceURL    string `json:"source_url"`
	QuotePrefix  string `json:"quote_prefix"`
	Decision     string `json:"decision"` // KEEP, DELETE_SOURCE, DELETE_FACT_CASCADE, SKIP_SCRAPE_FAILED
	Reason       string `json:"reason,omitempty"`
}

// FactSourceDeleter deletes one fact_sources row. *storage.PostgresStorer
// implements it.
type FactSourceDeleter interface {
	DeleteFactSourceByID(ctx context.Context, id int64) error
}

// AuditFactSourceAttribution re-scrapes the source and deletes the row when
// its stored original_quote can no longer be found in the cleaned content.
// A source without a stored quote is kept (nothing to verify); a scrape or
// delete failure is reported as SKIP_SCRAPE_FAILED and leaves the row alone.
func AuditFactSourceAttribution(ctx context.Context, ext extractor.ContentExtractor, storer FactSourceDeleter, r FactSourceForAudit) AttributionDecision {
	d := AttributionDecision{
		FactID:       r.FactID,
		FactSourceID: r.ID,
		SourceURL:    r.SourceURL,
		QuotePrefix:  util.TruncateString(r.OriginalQuote, 80),
	}

	// Skip if the stored quote is empty — we can't verify what isn't there.
	// Rare; treat as KEEP rather than risk losing legitimate facts.
	if strings.TrimSpace(r.OriginalQuote) == "" {
		d.Decision = AttributionKeep
		d.Reason = "no original_quote stored"
		return d
	}

	content, err := ext.ExtractContent(r.SourceURL)
	if err != nil {
		d.Decision = AttributionSkipScrapeFailed
		d.Reason = err.Error()
		return d
	}

	if quoteInContent(r.OriginalQuote, content.Content) {
		d.Decision = AttributionKeep
		return d
	}

	if err := storer.DeleteFactSourceByID(ctx, r.ID); err != nil {
		d.Decision = AttributionSkipScrapeFailed
		d.Reason = "delete failed: " + err.Error()
		return d
	}
	d.Decision = AttributionDeleteSource
	d.Reason = "quote not in cleaned content"
	return d
}

// quoteInContent does a normalized substring check. Normalization folds accents,
// lowercases, unifies smart quotes, and collapses whitespace — making the
// comparison robust to scraper-side punctuation differences.
func quoteInContent(quote, content string) bool {
	nq := normalizeForMatch(quote)
	nc := normalizeForMatch(content)
	if nq == "" || nc == "" {
		return false
	}
	// Strip leading/trailing ellipses so partial-quote citations still match.
	nq = strings.Trim(nq, ".")
	if len(nq) < 10 {
		return false
	}
	return strings.Contains(nc, nq)
}

// quoteUnifier folds smart quotes, guillemets and the ellipsis to ASCII.
var quoteUnifier = strings.NewReplacer(
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
	"«", `"`, "»", `"`,
	"…", "...",
)

// normalizeForMatch applies the transformations needed to compare a stored
// original_quote against re-scraped content tolerantly: lowercase, unify
// smart quotes / guillemets / ellipsis, drop accents (so "déclare" matches
// "declare" in a stripped corpus), collapse whitespace.
func normalizeForMatch(s string) string {
	if s == "" {
		return ""
	}
	s = quoteUnifier.Replace(strings.ToLower(s))
	s = util.RemoveAccents(s)
	return strings.Join(strings.Fields(s), " ")
}
