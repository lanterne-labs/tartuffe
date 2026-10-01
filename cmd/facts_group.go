package cmd

import (
	"context"
	"fmt"
	"slices"

	"digital-mirror/backend/deps"

	"github.com/spf13/cobra"
)

var (
	factsGroupIDs     []int64
	factsGroupApply   bool
	factsUngroupIDs   []int64
	factsUngroupApply bool
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

var factsUngroupCmd = &cobra.Command{
	Use:   "ungroup",
	Short: "Take facts out of the claim group they belong to",
	Long: `The counterpart of dm facts group: each given member leaves its claim group
and becomes an active fact again, in its parent's stance; the nightly detection
then judges it on its own. A given group parent dissolves its whole group.
Nothing is deleted. Dry run by default.

Examples:
  ./dm facts ungroup --facts 27876,22689
  ./dm facts ungroup --facts 27876,22689 --apply`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsUngroup,
}

func init() {
	factsCmd.AddCommand(factsUngroupCmd)
	factsUngroupCmd.Flags().Int64SliceVar(&factsUngroupIDs, "facts", nil, "Members to un-group, or parents whose group to dissolve")
	factsUngroupCmd.Flags().BoolVar(&factsUngroupApply, "apply", false, "Un-group them (default: show what would be done)")
	_ = factsUngroupCmd.MarkFlagRequired("facts")

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

func runFactsUngroup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	type pair struct{ parent, member int64 }
	var pairs []pair
	for _, id := range factsUngroupIDs {
		parent, err := storer.ClaimGroupParent(ctx, id)
		if err != nil {
			return err
		}
		if parent != id {
			pairs = append(pairs, pair{parent, id})
			continue
		}
		members, err := storer.GetGroupMembers(ctx, id)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			fmt.Printf("Fact %d is not in a claim group; skipped.\n", id)
		}
		for _, m := range members {
			pairs = append(pairs, pair{id, m.ID})
		}
	}
	for _, p := range pairs {
		fmt.Printf("  fact %d leaves the group of %d\n", p.member, p.parent)
	}
	if !factsUngroupApply {
		fmt.Println("[DRY-RUN] No changes were made to the database")
		return nil
	}
	for _, p := range pairs {
		if err := storer.UngroupFact(ctx, p.parent, p.member); err != nil {
			return fmt.Errorf("un-grouping fact %d from %d: %w", p.member, p.parent, err)
		}
	}
	if _, err := storer.RecomputeAllHonestyScores(ctx, false); err != nil {
		return fmt.Errorf("recomputing honesty scores: %w", err)
	}
	fmt.Printf("Un-grouped %d facts.\n", len(pairs))
	return nil
}
