package core

import (
	"context"
	"digital-mirror/backend/util"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
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

// Record converts the target to a PersonRecord (ID 0 when PersonID is unset).
func (t ResolvedTarget) Record() PersonRecord {
	rec := PersonRecord{Name: t.Name, EntityType: t.EntityType}
	if t.PersonID != nil {
		rec.ID = *t.PersonID
	}
	return rec
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

// ExactMatchOnly is the fuzzyFloor that makes ResolvePersonID accept exact
// matches only: the fuzzy similarity score reaches 1 solely for names that
// ResolveTargetName already reports as an exact match.
const ExactMatchOnly = 1.0

// PersonNotFoundError is returned by ResolvePersonID when no person matches
// the name at the requested confidence. Candidates holds the fuzzy matches
// that fell below the floor, best first.
type PersonNotFoundError struct {
	Name       string
	Candidates []MatchCandidate
}

func (e *PersonNotFoundError) Error() string {
	if len(e.Candidates) == 0 {
		return fmt.Sprintf("person %q not found in database", e.Name)
	}
	shown := e.Candidates
	if len(shown) > 3 {
		shown = shown[:3]
	}
	parts := make([]string, 0, len(shown))
	for _, c := range shown {
		parts = append(parts, fmt.Sprintf("%s (%.2f)", c.Target.Name, c.Score))
	}
	return fmt.Sprintf("no confident match for %q; closest: %s", e.Name, strings.Join(parts, ", "))
}

// ResolvePersonID resolves a name to one person: an exact match, else the
// best fuzzy match scoring at least fuzzyFloor, else a *PersonNotFoundError
// carrying the rejected candidates. Lookup failures are returned wrapped.
func ResolvePersonID(ctx context.Context, resolver PersonResolver, name string, fuzzyFloor float64) (PersonRecord, error) {
	result := ResolveTargetName(ctx, resolver, name)
	if result.Error != nil {
		return PersonRecord{}, fmt.Errorf("resolve person %q: %w", name, result.Error)
	}
	if result.IsExactMatch {
		return result.ExactMatch.Record(), nil
	}
	if len(result.FuzzyMatches) > 0 && result.FuzzyMatches[0].Score >= fuzzyFloor {
		top := result.FuzzyMatches[0]
		log.Info().Str("query", name).Str("matched", top.Target.Name).Float64("score", top.Score).Msg("Using fuzzy match")
		return top.Target.Record(), nil
	}
	return PersonRecord{}, &PersonNotFoundError{Name: name, Candidates: result.FuzzyMatches}
}

// ResolvePersons resolves each name with ResolvePersonID. Blank names and
// repeated persons are dropped; names that resolve to nobody are logged and
// skipped; a lookup failure aborts.
func ResolvePersons(ctx context.Context, resolver PersonResolver, names []string, fuzzyFloor float64) ([]PersonRecord, error) {
	var out []PersonRecord
	var unresolved []string
	seen := make(map[int64]bool)
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		person, err := ResolvePersonID(ctx, resolver, name, fuzzyFloor)
		var notFound *PersonNotFoundError
		if errors.As(err, &notFound) {
			unresolved = append(unresolved, name)
			continue
		}
		if err != nil {
			return nil, err
		}
		if seen[person.ID] {
			continue
		}
		seen[person.ID] = true
		out = append(out, person)
	}
	if len(unresolved) > 0 {
		log.Warn().Strs("unresolved", unresolved).Int("count", len(unresolved)).Msg("Some names did not resolve to a person; skipping them")
	}
	return out, nil
}
