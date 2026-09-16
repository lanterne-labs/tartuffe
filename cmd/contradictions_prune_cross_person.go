package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var pruneCrossPersonApply bool

var contradictionsPruneCrossPersonCmd = &cobra.Command{
	Use:   "prune-cross-person",
	Short: "Find (and with --apply, dismiss) contradictions whose two facts concern different persons.",
	Long: `Scans all contradictions for cross-person pairs — where fact_a and fact_b
share no common subject (mentioned) person. Such a pair is a "different subjects"
false positive (e.g. a Patricia Lemoine fact paired against a Darmanin fact).

Without --apply this is a dry run that only lists the offending ids. With --apply
each is dismissed as a false positive: the contradiction is removed, the pair is
suppressed from future detection, and the owner's honesty score is recomputed.

The RecordContradiction guard now prevents new cross-person pairs at insert time,
so this command is for cleaning up rows recorded before that guard existed (and as
a re-runnable safety net).`,
	RunE: runPruneCrossPerson,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsPruneCrossPersonCmd)
	contradictionsPruneCrossPersonCmd.Flags().BoolVar(&pruneCrossPersonApply, "apply", false, "Dismiss the cross-person contradictions (default: dry run)")
}

func runPruneCrossPerson(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	ids, err := storer.FindCrossPersonContradictionIDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to find cross-person contradictions: %w", err)
	}

	if len(ids) == 0 {
		fmt.Println("No cross-person contradictions found.")
		return nil
	}

	fmt.Printf("Found %d cross-person contradiction(s): %v\n", len(ids), ids)
	if !pruneCrossPersonApply {
		fmt.Println("Dry run — re-run with --apply to dismiss them (removes + suppresses re-detection + recomputes scores).")
		return nil
	}

	dismissed := 0
	for _, id := range ids {
		if err := storer.DismissContradictionAsFalsePositive(ctx, id); err != nil {
			log.Error().Err(err).Int64("contradiction_id", id).Msg("Failed to dismiss cross-person contradiction")
			continue
		}
		dismissed++
		fmt.Printf("Dismissed contradiction %d (cross-person false positive)\n", id)
	}
	fmt.Printf("\nDismissed %d/%d cross-person contradictions.\n", dismissed, len(ids))
	return nil
}
