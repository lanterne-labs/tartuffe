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
	factsReviewGroupsSince      string
	factsReviewGroupsStartAfter int64
	factsReviewGroupsEndAt      int64
	factsReviewGroupsApply      bool
	factsReviewGroupsOnly       []int64
)

var factsReviewGroupsCmd = &cobra.Command{
	Use:   "review-groups",
	Short: "Re-judge claim-group memberships with the current grouping rule",
	Long: `Re-checks every claim group that gained a member since --since. A member
needs evidence with another fact of the group: free-text source quotes that
overlap, or publication dates at most 7 days apart. A member without it (a
vote, a restatement weeks later) leaves without an LLM call. The other
members are judged by the occurrence gate, which reads the quotes, dates and
outlets, against the group's parent and the members already kept, by
majority. Members that no longer belong are un-grouped (--apply): they become
active facts again, in the parent's stance. Nothing is deleted.

Examples:
  ./dm facts review-groups --since 2026-09-17
  ./dm facts review-groups --since 2026-09-17 --apply
  ./dm facts review-groups --since 2026-01-01 --groups 12780,21277 --apply
  ./dm facts review-groups --since 2020-01-01 --start-after 15524 --apply`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsReviewGroups,
}

func init() {
	factsCmd.AddCommand(factsReviewGroupsCmd)
	factsReviewGroupsCmd.Flags().StringVar(&factsReviewGroupsSince, "since", "", "Review groups that gained a member on or after YYYY-MM-DD (required)")
	factsReviewGroupsCmd.Flags().BoolVar(&factsReviewGroupsApply, "apply", false, "Un-group the members that no longer belong")
	factsReviewGroupsCmd.Flags().Int64SliceVar(&factsReviewGroupsOnly, "groups", nil, "Review only these group parents")
	factsReviewGroupsCmd.Flags().Int64Var(&factsReviewGroupsStartAfter, "start-after", 0, "Skip the groups whose parent id is at most this value (resume a cut run; groups go in parent id order)")
	factsReviewGroupsCmd.Flags().Int64Var(&factsReviewGroupsEndAt, "end-at", 0, "Skip the groups whose parent id is above this value (two runs on disjoint ranges share the work)")
	_ = factsReviewGroupsCmd.MarkFlagRequired("since")
}

func runFactsReviewGroups(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	since, err := time.Parse("2006-01-02", factsReviewGroupsSince)
	if err != nil {
		return fmt.Errorf("invalid --since (expected YYYY-MM-DD): %w", err)
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	judge, err := core.NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		return fmt.Errorf("occurrence gate required: %w", err)
	}

	stats, err := core.ReviewClaimGroups(ctx, storer, judge, core.GroupReviewOptions{Since: since, OnlyIDs: factsReviewGroupsOnly, Apply: factsReviewGroupsApply, StartAfter: factsReviewGroupsStartAfter, EndAt: factsReviewGroupsEndAt})
	if err != nil {
		return err
	}
	if factsReviewGroupsApply && stats.Ungrouped > 0 {
		// An un-grouped fact is active again for every person it mentions;
		// settle every affected score in one deterministic pass.
		if _, err := storer.RecomputeAllHonestyScores(ctx, false); err != nil {
			return fmt.Errorf("recomputing honesty scores after the review: %w", err)
		}
	}
	printResults("CLAIM GROUP REVIEW RESULTS", []resultRow{
		{Label: "Groups reviewed", Value: stats.Groups},
		{Label: "Intact", Value: stats.Intact},
		{Label: "Trimmed (some members un-grouped)", Value: stats.Trimmed},
		{Label: "Dissolved (every member un-grouped)", Value: stats.Dissolved},
		{Label: "Members out: no evidence in the group", Value: stats.NoEvidence},
		{Label: "Members un-grouped", Value: stats.Ungrouped},
		{Label: "Gate failures (skipped)", Value: stats.GateFailures},
	})
	if !factsReviewGroupsApply {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}
