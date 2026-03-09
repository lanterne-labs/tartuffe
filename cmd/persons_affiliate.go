package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"fmt"
	"net/http"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var personsAffiliateCmd = &cobra.Command{
	Use:         "affiliate",
	Short:       "Auto-discover party affiliations via Wikidata",
	Long:        `Looks up each human person on Wikidata (P102 = member of political party) and creates affiliations with matching parties in the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	Run:         affiliatePersons,
}

func init() {
	personsAffiliateCmd.Flags().Bool("dry-run", false, "Preview affiliations without creating them")
	personsCmd.AddCommand(personsAffiliateCmd)
}

func affiliatePersons(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	records, err := storer.GetAllPersonRecords(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get persons")
	}

	// Partition into humans and parties
	var humans []core.PersonRecord
	parties := make(map[string]core.PersonRecord) // name -> record (for logging)
	for _, r := range records {
		if r.EntityType == "human" {
			humans = append(humans, r)
		} else {
			parties[r.Name] = r
		}
	}

	log.Info().Int("humans", len(humans)).Int("parties", len(parties)).Msg("Starting Wikidata affiliation lookup")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	var created, skipped, noMatch, noParty int

	for i, human := range humans {
		if i > 0 {
			time.Sleep(1 * time.Second) // rate limit: be nice to Wikidata
		}

		log.Info().Str("person", human.Name).Msgf("Looking up Wikidata (%d/%d)", i+1, len(humans))

		result, err := search.LookupPartyAffiliation(ctx, httpClient, human.Name)
		if err != nil {
			log.Warn().Err(err).Str("person", human.Name).Msg("Wikidata lookup failed")
			skipped++
			continue
		}
		if result == nil {
			log.Warn().Str("person", human.Name).Msg("No Wikidata entity found")
			noMatch++
			continue
		}
		if len(result.PartyNames) == 0 {
			log.Info().Str("person", human.Name).Str("entity", result.EntityID).Msg("No current party affiliation on Wikidata")
			noParty++
			continue
		}

		for _, partyName := range result.PartyNames {
			// Try to match against DB parties
			resolution := core.ResolveTargetName(ctx, storer, partyName)
			if resolution.Error != nil {
				log.Warn().Err(resolution.Error).Str("party", partyName).Msg("Failed to resolve party")
				continue
			}

			var matchedTarget *core.ResolvedTarget
			if resolution.IsExactMatch {
				matchedTarget = resolution.ExactMatch
			} else if len(resolution.FuzzyMatches) > 0 && resolution.FuzzyMatches[0].Score >= 0.7 {
				matchedTarget = &resolution.FuzzyMatches[0].Target
			}

			if matchedTarget == nil || matchedTarget.PersonID == nil {
				log.Info().Str("person", human.Name).Str("wikidata_party", partyName).Msg("Party not in DB")
				continue
			}

			if matchedTarget.EntityType == "human" {
				log.Debug().Str("person", human.Name).Str("matched", matchedTarget.Name).Msg("Skipping: matched entity is human, not a party")
				continue
			}

			if dryRun {
				fmt.Printf("[dry-run] %s → %s\n", human.Name, matchedTarget.Name)
				created++
				continue
			}

			tx, err := storer.Pool().Begin(ctx)
			if err != nil {
				log.Error().Err(err).Msg("Failed to begin transaction")
				continue
			}
			err = storer.FindOrCreateAffiliation(ctx, tx, human.ID, *matchedTarget.PersonID, nil, nil, map[string]any{})
			if err != nil {
				log.Error().Err(err).Str("person", human.Name).Str("party", matchedTarget.Name).Msg("Failed to create affiliation")
				tx.Rollback(ctx)
				continue
			}
			if err := tx.Commit(ctx); err != nil {
				log.Error().Err(err).Msg("Failed to commit")
				continue
			}
			fmt.Printf("Created: %s → %s\n", human.Name, matchedTarget.Name)
			created++
		}
	}

	fmt.Printf("\nSummary: %d affiliations %s, %d lookup errors, %d not found on Wikidata, %d no party data\n",
		created, actionLabel(dryRun), skipped, noMatch, noParty)

	if !dryRun && created > 0 {
		log.Info().Msg("Recomputing all honesty scores...")
		updates, err := storer.RecomputeAllHonestyScores(ctx, false)
		if err != nil {
			log.Error().Err(err).Msg("Failed to recompute scores")
			return
		}
		fmt.Printf("Recomputed %d person scores\n", len(updates))
	}
}

func actionLabel(dryRun bool) string {
	if dryRun {
		return "would be created"
	}
	return "created"
}
