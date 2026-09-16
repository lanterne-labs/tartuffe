package news

import (
	"context"

	"digital-mirror/backend/plugins"

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

// PrioritizeTargets orders and filters targets based on their likelihood of having news coverage.
// Returns a prioritized list of targets to search.
func (p *Provider) PrioritizeTargets(ctx context.Context, targets []plugins.Target, opts PrioritizationOptions) ([]plugins.Target, error) {
	if len(targets) == 0 {
		return targets, nil
	}

	// Separate parties and persons
	var parties []plugins.Target
	var persons []plugins.Target
	for _, t := range targets {
		if t.IsParty() {
			parties = append(parties, t)
		} else {
			persons = append(persons, t)
		}
	}

	// If SkipNoResults is set, filter persons based on search history
	if opts.SkipNoResults {
		filteredPersons := make([]plugins.Target, 0, len(persons))
		for _, t := range persons {
			if t.PersonID == 0 {
				// No ID means we haven't searched before, include it
				filteredPersons = append(filteredPersons, t)
				continue
			}

			shouldSearch, err := p.store.ShouldSearchNews(ctx, t.PersonID, opts.LookbackDays)
			if err != nil {
				log.Warn().Err(err).Str("target", t.Name).Msg("Failed to check search history, including target")
				filteredPersons = append(filteredPersons, t)
				continue
			}

			if shouldSearch {
				filteredPersons = append(filteredPersons, t)
			} else {
				log.Debug().Str("target", t.Name).Msg("Skipping target with no prior results")
			}
		}
		persons = filteredPersons
	}

	// Combine based on PartiesFirst option
	var result []plugins.Target
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
