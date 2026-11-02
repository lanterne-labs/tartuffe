package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
)

// contradictionsDedupeCmd finds the recorded contradictions that restate an
// older one of the same person (the same contradiction counted again because
// one fact was paired with several reports of one position, mirror-mtn) with
// the restatement judge new pairs now go through, and dismisses them with
// --apply. The oldest row of each set stays.
var contradictionsDedupeCmd = &cobra.Command{
	Use:   "dedupe",
	Short: "Find (and with --apply dismiss) contradictions that restate an older one",
	RunE:  runContradictionsDedupe,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsDedupeCmd)
	contradictionsDedupeCmd.Flags().Bool("apply", false, "dismiss the restating rows (default: report only)")
	contradictionsDedupeCmd.Flags().String("person", "", "only this person (name)")
}

func runContradictionsDedupe(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")
	only, _ := cmd.Flags().GetString("person")

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	judge, err := core.NewRestatementJudgeFromEnv()
	if err != nil {
		return err
	}

	rows, err := storer.ListContradictionsForDedupe(ctx, only)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	restating := map[int64]int64{} // row -> the older row it restates
	for _, r := range rows {
		cands, err := storer.ContradictionsSharingFact(ctx, r.PersonID, r.FactA, r.FactB, r.Reason)
		if err != nil {
			return err
		}
		for _, c := range cands {
			// Only an older row that stands on its own can be restated.
			if c.ID == r.ID || !c.CreatedAt.Before(r.CreatedAt) && !(c.CreatedAt.Equal(r.CreatedAt) && c.ID < r.ID) {
				continue
			}
			if _, dup := restating[c.ID]; dup {
				continue
			}
			same, why, err := judge.SameContradiction(ctx, r.Person, c)
			if err != nil {
				return fmt.Errorf("judging %d against %d: %w", r.ID, c.ID, err)
			}
			if same {
				restating[r.ID] = c.ID
				fmt.Printf("%d restates %d (%s): %s\n", r.ID, c.ID, r.Person, why)
				break
			}
		}
	}
	dismissed := 0
	if apply {
		for id := range restating {
			if err := storer.DismissContradictionAsFalsePositive(ctx, id); err != nil {
				return fmt.Errorf("dismissing %d: %w", id, err)
			}
			dismissed++
		}
	}
	fmt.Printf("contradictions: %d; restating an older one: %d; dismissed %d\n", len(rows), len(restating), dismissed)
	if !apply {
		fmt.Println("dry run: nothing dismissed (use --apply)")
	}
	return nil
}
