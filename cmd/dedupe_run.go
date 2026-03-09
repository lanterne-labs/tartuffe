package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
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
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	// Create storage adapter for deduplicator
	dedupeStorer := storage.NewDedupeStorerAdapter(storer)

	// Initialize LLM verifier (optional)
	var llmVerifier *core.DedupLLMVerifier
	if !dedupeSkipLLM {
		llmVerifier, err = core.NewDedupLLMVerifierFromEnv()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to create LLM verifier, proceeding without LLM verification")
		}
	}

	// Create deduplicator
	deduplicator := core.NewFactDeduplicator(dedupeStorer, llmVerifier)

	// Override thresholds from CLI flags if provided
	if cmd.Flags().Changed("auto-threshold") || cmd.Flags().Changed("uncertain-threshold") {
		deduplicator.SetThresholds(dedupeAutoThreshold, dedupeUncertainThreshold)
	}

	// Initialize embedder for regenerating embeddings after improvements
	if llmVerifier != nil {
		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to create embedder, fact improvements won't regenerate embeddings")
		} else {
			deduplicator.SetEmbedder(embedder)
		}
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
	fmt.Println()
	fmt.Println("=== DEDUPLICATION RESULTS ===")
	fmt.Printf("Total facts checked:    %d\n", result.TotalFactsChecked)
	fmt.Printf("Auto-deduped (>92%%):    %d\n", result.AutoDeduped)
	fmt.Printf("LLM verified dupes:     %d\n", result.LLMVerified)
	fmt.Printf("LLM kept separate:      %d\n", result.LLMKept)
	fmt.Printf("Facts improved:         %d\n", result.FactsImproved)
	fmt.Printf("Invalid facts deleted:  %d\n", result.FactsDeleted)

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

	// Print improvements if any
	if len(result.Improvements) > 0 {
		fmt.Println("\n--- Fact Improvements ---")
		for i, imp := range result.Improvements {
			if i >= 10 {
				fmt.Printf("\n... and %d more improvements\n", len(result.Improvements)-10)
				break
			}
			fmt.Printf("\nImprovement %d (Fact ID: %d):\n", i+1, imp.ID)
			fmt.Printf("  Before: %s\n", truncateForDisplay(imp.OldText, 80))
			fmt.Printf("  After:  %s\n", truncateForDisplay(imp.NewText, 80))
			fmt.Printf("  Reason: %s\n", imp.Reason)
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
			fmt.Printf("  Text:   %s\n", truncateForDisplay(del.Text, 80))
			fmt.Printf("  Reason: %s\n", del.Reason)
		}
	}
}

func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
