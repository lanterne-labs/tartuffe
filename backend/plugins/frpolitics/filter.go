package frpolitics

import (
	"context"
	"strings"
)

// Patterns indicating generic/procedural laws to skip
var genericPatterns = []string{
	"amendement n°",             // amendments lack standalone meaning
	"projet de loi de finances", // budget laws are too broad
	"examen prioritaire",        // procedural motion
	"sous-amendement",           // sub-amendment
	"rappel au règlement",       // procedural recall
}

// Patterns indicating specific/meaningful laws to KEEP
var specificPatterns = []string{
	"motion de censure", // political action
	"visant à",          // "aiming to" indicates purpose
	"pour sortir",       // "to exit" (e.g., narcotraffic)
	"lutter contre",     // "fight against"
	"réforme",           // "reform"
}

// classifyLaw determines if a law should be skipped based on keyword heuristics
// Returns: (skip, reason, pattern)
// - skip=true: law should be skipped
// - reason: "keyword_match" for generic patterns
// - pattern: the matched pattern string
func classifyLaw(title string) (skip bool, reason string, pattern string) {
	lower := strings.ToLower(title)

	// First check if it matches a specific pattern -> KEEP
	for _, p := range specificPatterns {
		if strings.Contains(lower, p) {
			return false, "", ""
		}
	}

	// Then check if it matches a generic pattern -> SKIP
	for _, p := range genericPatterns {
		if strings.Contains(lower, p) {
			return true, "keyword_match", p
		}
	}

	// Ambiguous -> needs AI
	return false, "needs_ai", ""
}

// FilterLaws applies keyword-based filtering and stores skipped laws in the database
func FilterLaws(ctx context.Context, laws []Law, store *Store) ([]Law, error) {
	var filtered []Law

	for _, law := range laws {
		skip, reason, pattern := classifyLaw(law.Title)

		if skip {
			// Store skipped law
			var aiConfidence *float64
			if reason == "keyword_match" {
				aiConfidence = nil // keyword-based, no AI confidence
			}

			err := store.StoreSkippedLaw(ctx, law.Key, law.Title, law.Legislature, law.VoteDate, reason, pattern, aiConfidence)
			if err != nil {
				return nil, err
			}
		} else {
			// Keep the law for further processing
			filtered = append(filtered, law)
		}
	}

	return filtered, nil
}
