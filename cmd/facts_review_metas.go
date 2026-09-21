package cmd

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"

	"github.com/spf13/cobra"
)

var (
	factsReviewMetasSince           string
	factsReviewMetasApply           bool
	factsReviewMetasDeleteDissolved bool
	factsReviewMetasOnly            []int64
)

var factsReviewMetasCmd = &cobra.Command{
	Use:   "review-metas",
	Short: "Re-judge meta-fact memberships with the current same-position gate",
	Long: `Re-checks every meta-fact created since --since: its members are judged
pairwise with the current fold gate and kept as a clique. Members that no
longer belong are unfolded (active again, re-routed to a stance by the nightly
orphan pass) and the meta text is rewritten from the members that stay
(--apply). A meta with fewer than two members left is a phantom: it is
reported, and deleted only with --delete-dissolved (its members become active
again through the cascade).

Examples:
  ./dm facts review-metas --since 2026-09-17
  ./dm facts review-metas --since 2026-09-17 --apply
  ./dm facts review-metas --since 2026-09-17 --apply --delete-dissolved`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsReviewMetas,
}

func init() {
	factsCmd.AddCommand(factsReviewMetasCmd)
	factsReviewMetasCmd.Flags().StringVar(&factsReviewMetasSince, "since", "", "Review meta-facts created on or after YYYY-MM-DD (required)")
	factsReviewMetasCmd.Flags().BoolVar(&factsReviewMetasApply, "apply", false, "Unfold the members that no longer belong and rewrite the meta text")
	factsReviewMetasCmd.Flags().BoolVar(&factsReviewMetasDeleteDissolved, "delete-dissolved", false, "With --apply: delete the metas whose members no longer hold together")
	factsReviewMetasCmd.Flags().Int64SliceVar(&factsReviewMetasOnly, "metas", nil, "Review only these meta ids (e.g. the dissolved list of a dry run)")
	_ = factsReviewMetasCmd.MarkFlagRequired("since")
}

func runFactsReviewMetas(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	since, err := time.Parse("2006-01-02", factsReviewMetasSince)
	if err != nil {
		return fmt.Errorf("invalid --since (expected YYYY-MM-DD): %w", err)
	}
	if factsReviewMetasDeleteDissolved && !factsReviewMetasApply {
		return fmt.Errorf("--delete-dissolved requires --apply")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	judge, err := core.NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		return fmt.Errorf("same-position gate required: %w", err)
	}
	synth, err := core.NewLLMMetaSynthesizerFromEnv()
	if err != nil {
		return fmt.Errorf("meta synthesizer required: %w", err)
	}

	stats, err := core.ReviewMetaFacts(ctx, storer, judge, synth, core.MetaReviewOptions{Since: since, OnlyIDs: factsReviewMetasOnly, Apply: factsReviewMetasApply})
	if err != nil {
		return err
	}
	rows := []resultRow{
		{Label: "Meta-facts reviewed", Value: stats.Metas},
		{Label: "Intact", Value: stats.Intact},
		{Label: "Trimmed (some members unfolded)", Value: stats.Trimmed},
		{Label: "Dissolved (phantom metas)", Value: stats.Dissolved},
		{Label: "Members unfolded", Value: stats.Unfolded},
		{Label: "Gate failures (skipped)", Value: stats.GateFailures},
	}
	if factsReviewMetasDeleteDissolved && len(stats.DissolvedIDs) > 0 {
		res, err := storer.DeleteFactsAndCleanup(ctx, stats.DissolvedIDs)
		if err != nil {
			return fmt.Errorf("deleting dissolved metas: %w", err)
		}
		rows = append(rows,
			resultRow{Label: "Dissolved metas deleted", Value: int64(res.FactsDeleted)},
			resultRow{Label: "Empty stances dropped", Value: int64(res.StancesDeleted)},
		)
	}
	printResults("META-FACT REVIEW RESULTS", rows)
	if !factsReviewMetasApply {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}
