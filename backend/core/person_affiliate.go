package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog/log"
)

// PartyAffiliation is what a lookup source knows about a person's parties.
type PartyAffiliation struct {
	EntityID   string   // identifier in the lookup source (e.g. a Wikidata Q-id)
	PartyNames []string // current party memberships, as named by the source
}

// PartyAffiliationLookup resolves a person name to their party affiliations;
// (nil, nil) means the person was not found. The Wikidata lookup lives in
// backend/search, which core does not import, so the caller wires it in.
type PartyAffiliationLookup func(ctx context.Context, personName string) (*PartyAffiliation, error)

// AffiliationStorer is the storage view AffiliatePersons needs.
// *storage.PostgresStorer implements it.
type AffiliationStorer interface {
	PersonResolver
	AddPersonWithType(ctx context.Context, name string, entityType string) (id int64, inserted bool, err error)
	// CreateAffiliation records (idempotently) that srcID is affiliated with dstID.
	CreateAffiliation(ctx context.Context, srcID, dstID int64) error
}

// AffiliateOptions tunes AffiliatePersons.
type AffiliateOptions struct {
	DryRun        bool
	AllowNewParty bool // create party entities the lookup returns but the DB lacks
}

// AffiliateStats are the counters of an AffiliatePersons pass.
type AffiliateStats struct {
	Created        int // affiliations created (or that would be, in dry run)
	PartiesCreated int
	LookupErrors   int
	NotFound       int // persons the lookup source does not know
	NoParty        int // persons found without party data
}

// affiliationPartyFuzzyFloor is the fuzzy-match score a DB party must reach to
// be taken for a party name returned by the lookup source.
const affiliationPartyFuzzyFloor = 0.7

// AffiliatePersons looks up every human person's party affiliations and
// creates affiliations with the matching parties in the database. Progress
// lines (one per affiliation) are written to out. Lookups are spaced one
// second apart to be nice to the source.
func AffiliatePersons(ctx context.Context, storer AffiliationStorer, lookup PartyAffiliationLookup, opts AffiliateOptions, out io.Writer) (AffiliateStats, error) {
	var stats AffiliateStats
	if out == nil {
		out = io.Discard
	}

	records, err := storer.GetAllPersonRecords(ctx)
	if err != nil {
		return stats, fmt.Errorf("failed to get persons: %w", err)
	}

	// Partition into humans and parties
	var humans []PersonRecord
	parties := 0
	for _, r := range records {
		if r.EntityType == "human" {
			humans = append(humans, r)
		} else {
			parties++
		}
	}

	log.Info().Int("humans", len(humans)).Int("parties", parties).Msg("Starting Wikidata affiliation lookup")

	for i, human := range humans {
		if i > 0 {
			time.Sleep(1 * time.Second) // rate limit: be nice to Wikidata
		}

		log.Info().Str("person", human.Name).Msgf("Looking up Wikidata (%d/%d)", i+1, len(humans))

		result, err := lookup(ctx, human.Name)
		if err != nil {
			log.Warn().Err(err).Str("person", human.Name).Msg("Wikidata lookup failed")
			stats.LookupErrors++
			continue
		}
		if result == nil {
			log.Warn().Str("person", human.Name).Msg("No Wikidata entity found")
			stats.NotFound++
			continue
		}
		if len(result.PartyNames) == 0 {
			log.Info().Str("person", human.Name).Str("entity", result.EntityID).Msg("No current party affiliation on Wikidata")
			stats.NoParty++
			continue
		}

		for _, partyName := range result.PartyNames {
			// Try to match against DB parties
			matched, err := ResolvePersonID(ctx, storer, partyName, affiliationPartyFuzzyFloor)
			var notFound *PersonNotFoundError
			if err != nil && !errors.As(err, &notFound) {
				log.Warn().Err(err).Str("party", partyName).Msg("Failed to resolve party")
				continue
			}

			if notFound != nil {
				if !opts.AllowNewParty {
					log.Info().Str("person", human.Name).Str("wikidata_party", partyName).Msg("Party not in DB")
					continue
				}
				// Create new party
				if opts.DryRun {
					fmt.Fprintf(out, "[dry-run] would create party: %s\n", partyName)
					fmt.Fprintf(out, "[dry-run] %s → %s\n", human.Name, partyName)
					stats.PartiesCreated++
					stats.Created++
					continue
				}
				partyID, inserted, err := storer.AddPersonWithType(ctx, partyName, "party")
				if err != nil {
					log.Error().Err(err).Str("party", partyName).Msg("Failed to create party")
					continue
				}
				if inserted {
					fmt.Fprintf(out, "Created party: %s (id=%d)\n", partyName, partyID)
					stats.PartiesCreated++
				}
				matched = PersonRecord{ID: partyID, Name: partyName, EntityType: "party"}
			}

			if matched.EntityType == "human" {
				log.Debug().Str("person", human.Name).Str("matched", matched.Name).Msg("Skipping: matched entity is human, not a party")
				continue
			}

			if opts.DryRun {
				fmt.Fprintf(out, "[dry-run] %s → %s\n", human.Name, matched.Name)
				stats.Created++
				continue
			}

			if err := storer.CreateAffiliation(ctx, human.ID, matched.ID); err != nil {
				log.Error().Err(err).Str("person", human.Name).Str("party", matched.Name).Msg("Failed to create affiliation")
				continue
			}
			fmt.Fprintf(out, "Created: %s → %s\n", human.Name, matched.Name)
			stats.Created++
		}
	}
	return stats, nil
}
