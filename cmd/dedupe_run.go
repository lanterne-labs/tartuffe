package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	dedupeTargetFlag         string
	dedupeDryRun             bool
	dedupeSkipLLM            bool
	dedupeAutoThreshold      float64
	dedupeUncertainThreshold float64
	dedupeBatchSize          int
)

var dedupeRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run deduplication on facts in the database",
	Long: `Run the multi-tier deduplication process on facts in the database.

The process:
1. For each person, find semantically similar facts
2. Auto-dedupe facts with >92% similarity (configurable)
3. Queue uncertain pairs (75-92%) for LLM verification
4. Keep facts with <75% similarity as separate

Examples:
  # Dedupe all facts
  ./dm dedupe run

  # Dedupe only facts for a specific person
  ./dm dedupe run --target "Marine Le Pen"

  # Dry run (show duplicates without marking them)
  ./dm dedupe run --dry-run

  # Skip LLM verification (faster, uses only embeddings)
  ./dm dedupe run --skip-llm

  # Custom thresholds
  ./dm dedupe run --auto-threshold 0.95 --uncertain-threshold 0.80`,
	RunE: runDedupe,
}

func init() {
	dedupeCmd.AddCommand(dedupeRunCmd)

	dedupeRunCmd.Flags().StringVarP(&dedupeTargetFlag, "target", "t", "", "Specific person name to dedupe (optional)")
	dedupeRunCmd.Flags().BoolVar(&dedupeDryRun, "dry-run", false, "Show duplicates without marking them")
	dedupeRunCmd.Flags().BoolVar(&dedupeSkipLLM, "skip-llm", false, "Skip LLM verification (faster, uses only embeddings)")
	dedupeRunCmd.Flags().Float64Var(&dedupeAutoThreshold, "auto-threshold", 0.92, "Similarity threshold for auto-deduplication")
	dedupeRunCmd.Flags().Float64Var(&dedupeUncertainThreshold, "uncertain-threshold", 0.75, "Lower bound similarity for LLM verification")
	dedupeRunCmd.Flags().IntVar(&dedupeBatchSize, "batch-size", 50, "Number of facts per LLM batch")
}

func runDedupe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	// Initialize storage
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	// Initialize LLM verifier (optional)
	var llmVerifier *core.DedupLLMVerifier
	if !dedupeSkipLLM {
		llmVerifier, err = core.NewDedupLLMVerifierFromEnv()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to create LLM verifier, proceeding without LLM verification")
		}
	}

	// Create deduplicator
	deduplicator := core.NewFactDeduplicator(storer, llmVerifier)

	// Override thresholds from CLI flags if provided
	if cmd.Flags().Changed("auto-threshold") || cmd.Flags().Changed("uncertain-threshold") {
		deduplicator.SetThresholds(dedupeAutoThreshold, dedupeUncertainThreshold)
	}

	// Build options
	opts := core.DedupeOptions{
		TargetName: dedupeTargetFlag,
		DryRun:     dedupeDryRun,
		SkipLLM:    dedupeSkipLLM || llmVerifier == nil,
		BatchSize:  dedupeBatchSize,
	}

	// Run deduplication
	var result *core.DedupeResult
	if dedupeTargetFlag != "" {
		// Normalize the target name
		normalizedName := util.NormalizeString(dedupeTargetFlag)
		log.Info().Str("target", normalizedName).Bool("dry_run", dedupeDryRun).Msg("Starting targeted deduplication")

		result, err = deduplicator.DedupeForPerson(ctx, normalizedName, opts)
	} else {
		log.Info().Bool("dry_run", dedupeDryRun).Msg("Starting full deduplication")
		result, err = deduplicator.DedupeAll(ctx, opts)
	}

	if err != nil {
		return fmt.Errorf("deduplication failed: %w", err)
	}

	// Print results
	printDedupeResults(result, dedupeDryRun)

	return nil
}

func printDedupeResults(result *core.DedupeResult, dryRun bool) {
	printResults("DEDUPLICATION RESULTS", []resultRow{
		{Label: "Total facts checked", Value: int64(result.TotalFactsChecked)},
		{Label: "Auto-deduped (>90%)", Value: int64(result.AutoDeduped)},
		{Label: "LLM verified dupes", Value: int64(result.LLMVerified)},
		{Label: "LLM kept separate", Value: int64(result.LLMKept)},
		{Label: "Invalid facts deleted", Value: int64(result.FactsDeleted)},
	})

	if dryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	// Print duplicate groups if any
	if len(result.DuplicateGroups) > 0 {
		fmt.Println("\n--- Duplicate Groups ---")
		for i, group := range result.DuplicateGroups {
			if i >= 10 {
				fmt.Printf("\n... and %d more groups\n", len(result.DuplicateGroups)-10)
				break
			}
			fmt.Printf("\nGroup %d:\n", i+1)
			fmt.Printf("  Canonical: %d\n", group.CanonicalID)
			fmt.Printf("  Duplicates: %v\n", group.DuplicateIDs)
			fmt.Printf("  Reason: %s\n", group.Reason)
			if group.Similarity > 0 {
				fmt.Printf("  Similarity: %.2f%%\n", group.Similarity*100)
			}
		}
	}

	// Print deletions if any
	if len(result.Deletions) > 0 {
		fmt.Println("\n--- Invalid Facts Deleted ---")
		for i, del := range result.Deletions {
			if i >= 10 {
				fmt.Printf("\n... and %d more deletions\n", len(result.Deletions)-10)
				break
			}
			fmt.Printf("\nDeleted %d (Fact ID: %d):\n", i+1, del.ID)
			fmt.Printf("  Text:   %s\n", util.TruncateString(del.Text, 80))
			fmt.Printf("  Reason: %s\n", del.Reason)
		}
	}
}
