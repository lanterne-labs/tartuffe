package cmd

import (
	"context"
	"fmt"
	"slices"

	"digital-mirror/backend/deps"

	"github.com/spf13/cobra"
)

var (
	factsGroupIDs   []int64
	factsGroupApply bool
)

var factsGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Put facts that state the same claim into one claim group",
	Long: `Groups the given facts under one claim group, whatever the same-position gate
decided: each fact (or the group it already belongs to) joins the group of the
most concrete of them, which becomes the parent. Their contradictions move to
the parent (duplicate pairs are dropped) and the honesty scores are recomputed.
Nothing is deleted; dm facts review-groups can un-group a member later.
Dry run by default.

Examples:
  ./dm facts group --facts 9998,9999,10191,10542,10546
  ./dm facts group --facts 9998,9999,10191,10542,10546 --apply`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsGroup,
}

func init() {
	factsCmd.AddCommand(factsGroupCmd)
	factsGroupCmd.Flags().Int64SliceVar(&factsGroupIDs, "facts", nil, "Facts to group (at least two)")
	factsGroupCmd.Flags().BoolVar(&factsGroupApply, "apply", false, "Group them (default: show what would be done)")
	_ = factsGroupCmd.MarkFlagRequired("facts")
}

func runFactsGroup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	var parents []int64
	for _, id := range factsGroupIDs {
		parent, err := storer.ClaimGroupParent(ctx, id)
		if err != nil {
			return err
		}
		if !slices.Contains(parents, parent) {
			parents = append(parents, parent)
		}
	}
	if len(parents) < 2 {
		fmt.Printf("Facts %v already form one claim group (parent %d); nothing to do.\n", factsGroupIDs, parents[0])
		return nil
	}
	into, err := storer.MostConcreteFact(ctx, parents)
	if err != nil {
		return err
	}
	fmt.Printf("Groups %v → parent %d (the most concrete wording)\n", parents, into)
	if !factsGroupApply {
		fmt.Println("[DRY-RUN] No changes were made to the database")
		return nil
	}
	for _, from := range parents {
		if from == into {
			continue
		}
		if err := storer.MergeFactIntoGroup(ctx, from, into); err != nil {
			return fmt.Errorf("grouping fact %d into %d: %w", from, into, err)
		}
		fmt.Printf("  fact %d grouped into %d\n", from, into)
	}
	if _, err := storer.RecomputeAllHonestyScores(ctx, false); err != nil {
		return fmt.Errorf("recomputing honesty scores: %w", err)
	}
	return nil
}
