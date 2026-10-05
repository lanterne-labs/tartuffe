package core

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// A claim group holds the reports of ONE occurrence: the same claim made, the
// same act done or the same event, on the same occasion. It is shown as
// "reported N times" with its outlets. The same position stated again on
// another occasion is a new occurrence; the stance holds those together.
//
// Two facts are grouped when both conditions hold:
//   - evidence: their source quotes overlap (groupQuoteSimilarityFloor) or
//     their publication dates are at most groupMaxDateGapDays apart. Storage
//     applies this rule (FindSimilarActiveFacts, ListGroupEvidencePairs).
//   - the LLM gate, which reads each fact with its quotes, dates and outlets,
//     says they are one occurrence, in both orders (IsSameOccurrence).
//
// Measured on 207 prod pairs the old text-only gate grouped (2026-10-05,
// labels made with the full evidence): this rule finds 93 % of the true
// groups; 5 % of its groups are clear errors and 12 % are borderline. The
// text-only gate had 27 % clear errors; quote overlap alone found 69 %.

// groupQuoteSimilarityFloor is the trigram similarity (pg_trgm) of two source
// quotes at or above which they count as overlapping.
const groupQuoteSimilarityFloor = 0.30

// groupMaxDateGapDays is the largest gap between two publication dates for
// which two reports can be one occurrence without a quote overlap. The gate
// reads the dates and still says "same" for restatements weeks apart, so the
// date rule is code, not prompt.
const groupMaxDateGapDays = 7

// ClaimSource is one source of a fact, as the grouping gate reads it.
type ClaimSource struct {
	Outlet      string     // host of the source URL, without "www."
	Quote       string     // verbatim source quote
	PublishedAt *time.Time // nil when unknown
}

// ClaimEvidence is a fact with the sources that support it.
type ClaimEvidence struct {
	Text    string
	Sources []ClaimSource
}

// OccurrenceVerdict is the grouping gate's answer for two facts.
type OccurrenceVerdict int

const (
	// OccurrenceDifferent: two occurrences, or two claims. Never grouped.
	OccurrenceDifferent OccurrenceVerdict = iota
	// OccurrenceSame: two reports of one claim, act or event.
	OccurrenceSame
	// OccurrenceContained: one occasion, and one fact holds the claim of the
	// other plus more. Grouped, with the fuller fact as the parent so that
	// no claim is hidden.
	OccurrenceContained
)

// Groupable reports whether the two facts go in one group.
func (v OccurrenceVerdict) Groupable() bool { return v != OccurrenceDifferent }

// SameOccurrenceJudge is the LLM gate deciding whether two facts report one
// occurrence (FactDedupeAdjudicator.IsSameOccurrence).
type SameOccurrenceJudge interface {
	IsSameOccurrence(ctx context.Context, a, b ClaimEvidence) (OccurrenceVerdict, error)
}

// OutletOf returns the host of a source URL without a leading "www.", or ""
// when the URL has no host.
func OutletOf(sourceURL string) string {
	u, err := url.Parse(sourceURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// fullerFact returns, of two fact texts, the index (0 or 1) of the longer
// one: for a contained pair, the fact that holds the other's claim plus more.
func fullerFact(a, b string) int {
	if len([]rune(b)) > len([]rune(a)) {
		return 1
	}
	return 0
}

// GroupEvidence is the evidence rule two facts meet before the gate reads
// them: an own source of one of SourceTypes on each side, with quotes of
// trigram similarity >= QuoteFloor or publication dates <= MaxDateGapDays
// apart.
type GroupEvidence struct {
	SourceTypes    []string
	QuoteFloor     float64
	MaxDateGapDays int
}

// groupEvidenceRule returns the rule in force: free-text sources only (a
// structured record such as a vote is identified by its record, so two of
// them are never one claim).
func groupEvidenceRule() (GroupEvidence, error) {
	types := freeTextSourceTypes()
	if len(types) == 0 {
		return GroupEvidence{}, errors.New("no free-text source type is registered: no fact can be grouped")
	}
	return GroupEvidence{SourceTypes: types, QuoteFloor: groupQuoteSimilarityFloor, MaxDateGapDays: groupMaxDateGapDays}, nil
}

// ClaimEvidenceStorer loads what the gate reads and applies the evidence
// rule to pairs. *storage.PostgresStorer implements it.
type ClaimEvidenceStorer interface {
	// LoadClaimSources returns, per fact, its sources of sourceTypes (oldest
	// first, at most 3 per fact).
	LoadClaimSources(ctx context.Context, factIDs []int64, sourceTypes []string) (map[int64][]ClaimSource, error)
	// ListGroupEvidencePairs returns the subset of pairs (as given) that
	// meet the evidence rule.
	ListGroupEvidencePairs(ctx context.Context, pairs [][2]int64, rule GroupEvidence) ([][2]int64, error)
}
