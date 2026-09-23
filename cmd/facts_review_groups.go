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
	factsReviewGroupsSince string
	factsReviewGroupsApply bool
	factsReviewGroupsOnly  []int64
)

var factsReviewGroupsCmd = &cobra.Command{
	Use:   "review-groups",
	Short: "Re-judge claim-group memberships with the current same-position gate",
	Long: `Re-checks every claim group that gained a member since --since: each member is
judged against the group's parent and the members already kept, by majority,
with order-swap agreement on a "different" verdict. Members that no longer
belong are un-grouped (--apply): they become active facts again, in the
parent's stance. Nothing is deleted.

Examples:
  ./dm facts review-groups --since 2026-09-17
  ./dm facts review-groups --since 2026-09-17 --apply
  ./dm facts review-groups --since 2026-01-01 --groups 12780,21277 --apply`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsReviewGroups,
}

func init() {
	factsCmd.AddCommand(factsReviewGroupsCmd)
	factsReviewGroupsCmd.Flags().StringVar(&factsReviewGroupsSince, "since", "", "Review groups that gained a member on or after YYYY-MM-DD (required)")
	factsReviewGroupsCmd.Flags().BoolVar(&factsReviewGroupsApply, "apply", false, "Un-group the members that no longer belong")
	factsReviewGroupsCmd.Flags().Int64SliceVar(&factsReviewGroupsOnly, "groups", nil, "Review only these group parents")
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
		return fmt.Errorf("same-position gate required: %w", err)
	}

	stats, err := core.ReviewClaimGroups(ctx, storer, judge, core.GroupReviewOptions{Since: since, OnlyIDs: factsReviewGroupsOnly, Apply: factsReviewGroupsApply})
	if err != nil {
		return err
	}
	printResults("CLAIM GROUP REVIEW RESULTS", []resultRow{
		{Label: "Groups reviewed", Value: stats.Groups},
		{Label: "Intact", Value: stats.Intact},
		{Label: "Trimmed (some members un-grouped)", Value: stats.Trimmed},
		{Label: "Dissolved (every member un-grouped)", Value: stats.Dissolved},
		{Label: "Members un-grouped", Value: stats.Ungrouped},
		{Label: "Gate failures (skipped)", Value: stats.GateFailures},
	})
	if !factsReviewGroupsApply {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}
