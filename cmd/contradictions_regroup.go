package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	// Build the LLM-backed summarizer unless skipped.
	var summarize storage.GroupSummarizer
	if !regroupNoSummary {
		detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create contradiction detector (use --no-summary to skip LLM): %w", err)
		}
		summarize = func(ctx context.Context, baseFact string, spokes []storage.GroupSpokeInput) (string, string, error) {
			ls := make([]llm.GroupSpoke, len(spokes))
			for i, sp := range spokes {
				ls[i] = llm.GroupSpoke{FactText: sp.FactText, Reason: sp.Reason}
			}
			return detector.SynthesizeGroupSummary(ctx, baseFact, ls)
		}
	}

	// Resolve the target person set.
	var personIDs []int64
	switch {
	case regroupPersonIDFlag > 0:
		personIDs = []int64{regroupPersonIDFlag}
	case regroupPersonFlag != "":
		id, err := storer.GetPersonIDByName(ctx, util.NormalizeString(regroupPersonFlag))
		if err != nil {
			return fmt.Errorf("failed to find person %q: %w", regroupPersonFlag, err)
		}
		personIDs = []int64{id}
	default:
		personIDs, err = storer.PersonIDsWithContradictions(ctx)
		if err != nil {
			return fmt.Errorf("failed to list persons with contradictions: %w", err)
		}
	}

	agg := regroupPersons(ctx, storer, personIDs, summarize)
	fmt.Fprintf(os.Stderr, "Regroup complete: %d person(s), %d rows rebased, %d multi-fact groups, %d summaries written, %d summary failures.\n",
		len(personIDs), agg.Rebased, agg.Groups, agg.SummariesWrote, agg.SummariesFailed)
	return nil
}

// regroupPersons runs RegroupPersonContradictions over each person id and returns the
// aggregate result. A per-person error is logged and skipped so one bad person never
// aborts a full sweep (used by both the CLI and the daily pipeline).
func regroupPersons(ctx context.Context, storer *storage.PostgresStorer, personIDs []int64, summarize storage.GroupSummarizer) storage.RegroupResult {
	var agg storage.RegroupResult
	for _, pid := range personIDs {
		res, err := storer.RegroupPersonContradictions(ctx, pid, summarize)
		if err != nil {
			log.Error().Err(err).Int64("person_id", pid).Msg("regroup: person failed, skipping")
			continue
		}
		agg.Rebased += res.Rebased
		agg.Groups += res.Groups
		agg.SummariesWrote += res.SummariesWrote
		agg.SummariesFailed += res.SummariesFailed
		if res.Rebased > 0 || res.Groups > 0 {
			log.Info().Int64("person_id", pid).Int("rebased", res.Rebased).
				Int("groups", res.Groups).Int("summaries", res.SummariesWrote).
				Int("summary_failures", res.SummariesFailed).Msg("regrouped contradictions")
		}
	}
	return agg
}

// runContradictionRegroupStep is the pipeline entry point: regroup persons with
// contradictions, generating merged summaries via the LLM. scopeIDs restricts it
// to those persons (nil = all). Best-effort — a nil detector (no LLM env)
// downgrades to base-only rather than failing the pipeline.
func runContradictionRegroupStep(ctx context.Context, storer *storage.PostgresStorer, scopeIDs []int64) error {
	personIDs, err := storer.PersonIDsWithContradictions(ctx)
	if err != nil {
		return fmt.Errorf("list persons with contradictions: %w", err)
	}
	if len(scopeIDs) > 0 {
		want := make(map[int64]bool, len(scopeIDs))
		for _, id := range scopeIDs {
			want[id] = true
		}
		kept := personIDs[:0:0]
		for _, id := range personIDs {
			if want[id] {
				kept = append(kept, id)
			}
		}
		personIDs = kept
	}
	if len(personIDs) == 0 {
		return nil
	}

	var summarize storage.GroupSummarizer
	if detector, dErr := contradiction.NewOpenAIContradictionDetectorFromEnv(); dErr != nil {
		log.Warn().Err(dErr).Msg("regroup: no LLM detector; setting group bases without summaries")
	} else {
		summarize = func(ctx context.Context, baseFact string, spokes []storage.GroupSpokeInput) (string, string, error) {
			ls := make([]llm.GroupSpoke, len(spokes))
			for i, sp := range spokes {
				ls[i] = llm.GroupSpoke{FactText: sp.FactText, Reason: sp.Reason}
			}
			return detector.SynthesizeGroupSummary(ctx, baseFact, ls)
		}
	}

	agg := regroupPersons(ctx, storer, personIDs, summarize)
	log.Info().Int("persons", len(personIDs)).Int("rebased", agg.Rebased).
		Int("groups", agg.Groups).Int("summaries", agg.SummariesWrote).
		Int("summary_failures", agg.SummariesFailed).Msg("Pipeline: contradiction regroup done")
	return nil
}
