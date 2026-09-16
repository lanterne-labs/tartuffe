package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	regroupPersonFlag   string
	regroupPersonIDFlag int64
	regroupNoSummary    bool
)

var contradictionsRegroupCmd = &cobra.Command{
	Use:   "regroup",
	Short: "Collapse a person's pairwise contradictions into N-ary groups (one base fact + N contradicting facts).",
	Long: `Reshapes recorded contradictions into distinct-inconsistency groups: facts that
conflict on the same point are merged under a single base (anchor) fact via a
greedy "star peel" (the highest-degree fact in each connected component becomes
the base; the rest become its contradicting facts). Each row's base_fact_id pointer
is set to its group's base, and every multi-fact group gets one LLM-synthesized
merged summary (group_summary). The canonical (fact_a_id, fact_b_id) pair,
bundle_stance_id and honesty scores are untouched — this is a presentation-layer
collapse.

Runs for one person (--person / --person-id) or, with no flag, for every person
that has contradictions. Idempotent: re-running with no new contradictions makes
no base changes (it still refreshes multi-group summaries, which is free on the
local LLM). Pass --no-summary to skip the LLM and only set group bases.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionRegroup,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsRegroupCmd)
	contradictionsRegroupCmd.Flags().StringVarP(&regroupPersonFlag, "person", "p", "", "Restrict to a single person by name")
	contradictionsRegroupCmd.Flags().Int64Var(&regroupPersonIDFlag, "person-id", 0, "Restrict to a single person by numeric ID (overrides --person)")
	contradictionsRegroupCmd.Flags().BoolVar(&regroupNoSummary, "no-summary", false, "Only set group bases; skip the LLM merged-summary generation")
}

func runContradictionRegroup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	opts := contradiction.RegroupOptions{}

	// Build the LLM-backed summarizer unless skipped.
	if !regroupNoSummary {
		detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create contradiction detector (use --no-summary to skip LLM): %w", err)
		}
		opts.Summarize = contradiction.GroupSummarizer(detector)
	}

	// Resolve the target person set (empty = every person with contradictions).
	switch {
	case regroupPersonIDFlag > 0:
		opts.PersonIDs = []int64{regroupPersonIDFlag}
	case regroupPersonFlag != "":
		person, err := core.ResolvePersonID(ctx, storer, regroupPersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		opts.PersonIDs = []int64{person.ID}
	}

	agg, err := contradiction.RunRegroup(ctx, storer, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Regroup complete: %d person(s), %d rows rebased, %d multi-fact groups, %d summaries written, %d summary failures.\n",
		agg.Persons, agg.Rebased, agg.Groups, agg.SummariesWrote, agg.SummariesFailed)
	return nil
}
