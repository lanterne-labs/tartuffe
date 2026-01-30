package news

import (
	"context"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
)

// TargetResolutionResult represents the result of resolving a target name.
type TargetResolutionResult struct {
	// Query is the original query string
	Query string
	// IsExactMatch indicates if an exact match was found
	IsExactMatch bool
	// ExactMatch is the exact match if found
	ExactMatch *SearchTarget
	// FuzzyMatches are potential matches if no exact match was found
	FuzzyMatches []MatchCandidate
	// Error is any error that occurred during resolution
	Error error
}

// MatchCandidate represents a potential match for a query.
type MatchCandidate struct {
	Target SearchTarget
	Score  float64
}

// ResolveTargetName attempts to resolve a name to a SearchTarget.
// If an exact match is found, it returns that.
// If no exact match is found, it returns fuzzy matches for disambiguation.
func ResolveTargetName(ctx context.Context, storer *storage.PostgresStorer, query string) TargetResolutionResult {
	result := TargetResolutionResult{
		Query: query,
	}

	// First, try exact match (case-insensitive)
	allPersons, err := storer.GetAllPersons(ctx)
	if err != nil {
		result.Error = fmt.Errorf("failed to get persons: %w", err)
		return result
	}

	// Normalize query for exact matching
	normalizedQuery := util.NormalizeForFuzzy(query)

	for _, p := range allPersons {
		normalizedName := util.NormalizeForFuzzy(p.Name)
		if normalizedQuery == normalizedName {
			// Exact match found
			targetType := TargetPerson
			if p.EntityType == "party" {
				targetType = TargetParty
			}

			personID := p.ID
			result.IsExactMatch = true
			result.ExactMatch = &SearchTarget{
				Name:     p.Name,
				Type:     targetType,
				PersonID: &personID,
			}
			return result
		}
	}

	// No exact match, try fuzzy matching
	matches, err := storer.FindPersonsByFuzzyName(ctx, query, 0.5, 10) // threshold 0.5, max 10 results
	if err != nil {
		result.Error = fmt.Errorf("failed to fuzzy match: %w", err)
		return result
	}

	// Convert to match candidates
	result.FuzzyMatches = make([]MatchCandidate, 0, len(matches))
	for _, m := range matches {
		targetType := TargetPerson
		if m.EntityType == "party" {
			targetType = TargetParty
		}

		personID := m.ID
		result.FuzzyMatches = append(result.FuzzyMatches, MatchCandidate{
			Target: SearchTarget{
				Name:     m.Name,
				Type:     targetType,
				PersonID: &personID,
			},
			Score: m.Score,
		})
	}

	return result
}

// ResolveTargetNames resolves multiple target names.
// Returns a map of query -> resolution result.
func ResolveTargetNames(ctx context.Context, storer *storage.PostgresStorer, queries []string) map[string]TargetResolutionResult {
	results := make(map[string]TargetResolutionResult)
	for _, query := range queries {
		results[query] = ResolveTargetName(ctx, storer, query)
	}
	return results
}
