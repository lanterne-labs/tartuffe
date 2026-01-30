package news

import (
	"context"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// PrioritizationOptions configures how targets are prioritized for news search.
type PrioritizationOptions struct {
	// LookbackDays is the number of days to look back for search history.
	LookbackDays int
	// SkipNoResults skips targets that had no results in previous searches.
	SkipNoResults bool
	// PartiesFirst ensures parties are searched before persons.
	PartiesFirst bool
}

// DefaultPrioritizationOptions returns sensible defaults for prioritization.
func DefaultPrioritizationOptions() PrioritizationOptions {
	return PrioritizationOptions{
		LookbackDays:  30,
		SkipNoResults: true,
		PartiesFirst:  true,
	}
}

// PrioritizeTargets orders and filters targets based on their likelihood of having news coverage.
// Returns a prioritized list of targets to search.
func PrioritizeTargets(ctx context.Context, storer *storage.PostgresStorer, targets []SearchTarget, opts PrioritizationOptions) ([]SearchTarget, error) {
	if len(targets) == 0 {
		return targets, nil
	}

	// Separate parties and persons
	var parties []SearchTarget
	var persons []SearchTarget
	for _, t := range targets {
		if t.Type == TargetParty {
			parties = append(parties, t)
		} else {
			persons = append(persons, t)
		}
	}

	// If SkipNoResults is set, filter persons based on search history
	if opts.SkipNoResults {
		filteredPersons := make([]SearchTarget, 0, len(persons))
		for _, p := range persons {
			if p.PersonID == nil {
				// No ID means we haven't searched before, include it
				filteredPersons = append(filteredPersons, p)
				continue
			}

			shouldSearch, err := storer.ShouldSearchNews(ctx, *p.PersonID, opts.LookbackDays)
			if err != nil {
				log.Warn().Err(err).Str("target", p.Name).Msg("Failed to check search history, including target")
				filteredPersons = append(filteredPersons, p)
				continue
			}

			if shouldSearch {
				filteredPersons = append(filteredPersons, p)
			} else {
				log.Debug().Str("target", p.Name).Msg("Skipping target with no prior results")
			}
		}
		persons = filteredPersons
	}

	// Combine based on PartiesFirst option
	var result []SearchTarget
	if opts.PartiesFirst {
		result = append(result, parties...)
		result = append(result, persons...)
	} else {
		result = append(result, persons...)
		result = append(result, parties...)
	}

	log.Info().
		Int("total_input", len(targets)).
		Int("parties", len(parties)).
		Int("persons", len(persons)).
		Int("total_output", len(result)).
		Msg("Prioritized news search targets")

	return result, nil
}

// BuildTargetsFromPersons creates SearchTarget entries from person records.
// personIDToName maps person IDs to their names.
// personIDToType maps person IDs to their entity types ("person" or "party").
func BuildTargetsFromPersons(personIDToName map[int64]string, personIDToType map[int64]string) []SearchTarget {
	targets := make([]SearchTarget, 0, len(personIDToName))
	for id, name := range personIDToName {
		targetType := TargetPerson
		if entityType, ok := personIDToType[id]; ok && entityType == "party" {
			targetType = TargetParty
		}

		personID := id // Create a copy for the pointer
		targets = append(targets, SearchTarget{
			Name:     name,
			Type:     targetType,
			PersonID: &personID,
		})
	}
	return targets
}
