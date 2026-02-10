package core

import (
	"context"
	"digital-mirror/backend/util"
	"fmt"
)

// PersonRecord holds basic person information for name resolution.
type PersonRecord struct {
	ID         int64
	Name       string
	EntityType string
}

// PersonResolver is the interface required by ResolveTargetName for looking up persons.
type PersonResolver interface {
	GetAllPersonRecords(ctx context.Context) ([]PersonRecord, error)
	FindPersonsByFuzzyName(ctx context.Context, query string, threshold float64, maxResults int) ([]util.PersonMatch, error)
}

// ResolvedTarget represents a resolved person/party from the database.
type ResolvedTarget struct {
	// Name is the person or party name as stored in the database.
	Name string
	// EntityType is "human", "party", or "organization".
	EntityType string
	// PersonID is the database ID.
	PersonID *int64
}

// TargetResolutionResult represents the result of resolving a target name.
type TargetResolutionResult struct {
	// Query is the original query string
	Query string
	// IsExactMatch indicates if an exact match was found
	IsExactMatch bool
	// ExactMatch is the exact match if found
	ExactMatch *ResolvedTarget
	// FuzzyMatches are potential matches if no exact match was found
	FuzzyMatches []MatchCandidate
	// Error is any error that occurred during resolution
	Error error
}

// MatchCandidate represents a potential match for a query.
type MatchCandidate struct {
	Target ResolvedTarget
	Score  float64
}

// ResolveTargetName attempts to resolve a name to a ResolvedTarget.
// If an exact match is found, it returns that.
// If no exact match is found, it returns fuzzy matches for disambiguation.
func ResolveTargetName(ctx context.Context, resolver PersonResolver, query string) TargetResolutionResult {
	result := TargetResolutionResult{
		Query: query,
	}

	// First, try exact match (case-insensitive)
	allPersons, err := resolver.GetAllPersonRecords(ctx)
	if err != nil {
		result.Error = fmt.Errorf("failed to get persons: %w", err)
		return result
	}

	// Normalize query for exact matching
	normalizedQuery := util.NormalizeForFuzzy(query)

	for _, p := range allPersons {
		normalizedName := util.NormalizeForFuzzy(p.Name)
		if normalizedQuery == normalizedName {
			personID := p.ID
			result.IsExactMatch = true
			result.ExactMatch = &ResolvedTarget{
				Name:       p.Name,
				EntityType: p.EntityType,
				PersonID:   &personID,
			}
			return result
		}
	}

	// No exact match, try fuzzy matching
	matches, err := resolver.FindPersonsByFuzzyName(ctx, query, 0.5, 10)
	if err != nil {
		result.Error = fmt.Errorf("failed to fuzzy match: %w", err)
		return result
	}

	// Convert to match candidates
	result.FuzzyMatches = make([]MatchCandidate, 0, len(matches))
	for _, m := range matches {
		personID := m.ID
		result.FuzzyMatches = append(result.FuzzyMatches, MatchCandidate{
			Target: ResolvedTarget{
				Name:       m.Name,
				EntityType: m.EntityType,
				PersonID:   &personID,
			},
			Score: m.Score,
		})
	}

	return result
}

// ResolveTargetNames resolves multiple target names.
// Returns a map of query -> resolution result.
func ResolveTargetNames(ctx context.Context, resolver PersonResolver, queries []string) map[string]TargetResolutionResult {
	results := make(map[string]TargetResolutionResult)
	for _, query := range queries {
		results[query] = ResolveTargetName(ctx, resolver, query)
	}
	return results
}
