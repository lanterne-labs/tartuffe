package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/search"
	"fmt"
	"net/http"
	"os"
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
	personsAffiliateCmd.Flags().Bool("allow-new-party", false, "Create new party entities in DB when Wikidata returns a party not yet tracked")
	personsCmd.AddCommand(personsAffiliateCmd)
}

func affiliatePersons(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	allowNewParty, _ := cmd.Flags().GetBool("allow-new-party")

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	httpClient := &http.Client{Timeout: 30 * time.Second}
	lookup := func(ctx context.Context, personName string) (*core.PartyAffiliation, error) {
		r, err := search.LookupPartyAffiliation(ctx, httpClient, personName)
		if err != nil || r == nil {
			return nil, err
		}
		return &core.PartyAffiliation{EntityID: r.EntityID, PartyNames: r.PartyNames}, nil
	}

	stats, err := core.AffiliatePersons(ctx, storer, lookup, core.AffiliateOptions{DryRun: dryRun, AllowNewParty: allowNewParty}, os.Stdout)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to affiliate persons")
	}

	fmt.Printf("\nSummary: %d affiliations %s, %d parties %s, %d lookup errors, %d not found on Wikidata, %d no party data\n",
		stats.Created, actionLabel(dryRun), stats.PartiesCreated, actionLabel(dryRun), stats.LookupErrors, stats.NotFound, stats.NoParty)

	if !dryRun && stats.Created > 0 {
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
