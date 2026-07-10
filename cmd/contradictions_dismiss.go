package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"fmt"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var dismissIDsFlag string

var contradictionsDismissCmd = &cobra.Command{
	Use:   "dismiss",
	Short: "Dismiss specific contradictions by id as false positives.",
	Long: `Dismisses the given contradiction ids as false positives: each is removed, a
NEGATIVE fact_pair_checks row is written so the detector will not re-raise it, and
the owner's honesty score is recomputed.

Use this for a hand-picked set of false positives (e.g. the ones a review confirmed),
as opposed to 'dm contradictions check --apply' which evicts everything the LLM
auditor flags.

Example:
  dm contradictions dismiss --ids 182,183,184,178,190,174,171`,
	RunE: runContradictionsDismiss,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsDismissCmd)
	contradictionsDismissCmd.Flags().StringVar(&dismissIDsFlag, "ids", "", "Comma-separated contradiction ids to dismiss (required)")
}

func runContradictionsDismiss(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if strings.TrimSpace(dismissIDsFlag) == "" {
		return fmt.Errorf("--ids is required (comma-separated contradiction ids)")
	}

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var ids []int64
	for _, p := range strings.Split(dismissIDsFlag, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid contradiction id %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return fmt.Errorf("no valid ids parsed from --ids")
	}

	dismissed := 0
	for _, id := range ids {
		if err := storer.DismissContradictionAsFalsePositive(ctx, id); err != nil {
			log.Error().Err(err).Int64("contradiction_id", id).Msg("Failed to dismiss contradiction")
			continue
		}
		dismissed++
		fmt.Printf("Dismissed contradiction %d (false positive)\n", id)
	}
	fmt.Printf("\nDismissed %d/%d contradictions.\n", dismissed, len(ids))
	return nil
}
