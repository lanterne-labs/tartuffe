package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	checkTargetFlag  string
	checkOutputJSON  bool
	checkShowDetails bool
)

var dedupeCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Preview potential duplicates without making changes",
	Long: `Check for potential duplicate facts in the database without marking them.

This is equivalent to 'dedupe run --dry-run --skip-llm' but with output
optimized for reviewing potential duplicates.

Examples:
  # Check all facts for duplicates
  ./dm dedupe check

  # Check facts for a specific person
  ./dm dedupe check --target "Marine Le Pen"

  # Output as JSON for scripting
  ./dm dedupe check --output json

  # Show detailed fact text
  ./dm dedupe check --details`,
	RunE: runDedupeCheck,
}

func init() {
	dedupeCmd.AddCommand(dedupeCheckCmd)

	dedupeCheckCmd.Flags().StringVarP(&checkTargetFlag, "target", "t", "", "Specific person name to check (optional)")
	dedupeCheckCmd.Flags().BoolVar(&checkOutputJSON, "output", false, "Output results as JSON")
	dedupeCheckCmd.Flags().BoolVar(&checkShowDetails, "details", false, "Show detailed fact text for each duplicate")
}

func runDedupeCheck(cmd *cobra.Command, args []string) error {
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

	// Create deduplicator without LLM (for fast checking)
	deduplicator := core.NewFactDeduplicator(dedupeStorer, nil)

	// Build options - always dry run, skip LLM for fast checking
	opts := core.DedupeOptions{
		TargetName: checkTargetFlag,
		DryRun:     true,
		SkipLLM:    true,
	}

	// Run deduplication check
	var result *core.DedupeResult
	if checkTargetFlag != "" {
		normalizedName := util.NormalizeString(checkTargetFlag)
		log.Info().Str("target", normalizedName).Msg("Checking for duplicates")
		result, err = deduplicator.DedupeForPerson(ctx, normalizedName, opts)
	} else {
		log.Info().Msg("Checking all facts for duplicates")
		result, err = deduplicator.DedupeAll(ctx, opts)
	}

	if err != nil {
		return fmt.Errorf("duplicate check failed: %w", err)
	}

	// Output results
	if checkOutputJSON {
		return outputCheckResultsJSON(result, storer, ctx)
	}

	outputCheckResultsText(result, storer, ctx)
	return nil
}

func outputCheckResultsJSON(result *core.DedupeResult, storer *storage.PostgresStorer, ctx context.Context) error {
	type jsonGroup struct {
		CanonicalID   int64   `json:"canonical_id"`
		CanonicalText string  `json:"canonical_text,omitempty"`
		DuplicateIDs  []int64 `json:"duplicate_ids"`
		Reason        string  `json:"reason"`
		Similarity    float64 `json:"similarity"`
	}

	type jsonOutput struct {
		TotalFactsChecked   int         `json:"total_facts_checked"`
		PotentialDuplicates int         `json:"potential_duplicates"`
		Groups              []jsonGroup `json:"groups"`
	}

	output := jsonOutput{
		TotalFactsChecked:   result.TotalFactsChecked,
		PotentialDuplicates: result.AutoDeduped,
		Groups:              make([]jsonGroup, 0, len(result.DuplicateGroups)),
	}

	for _, group := range result.DuplicateGroups {
		jg := jsonGroup{
			CanonicalID:  group.CanonicalID,
			DuplicateIDs: group.DuplicateIDs,
			Reason:       group.Reason,
			Similarity:   group.Similarity,
		}

		// Optionally fetch canonical fact text
		if checkShowDetails {
			fact, err := storer.GetFactByID(ctx, group.CanonicalID)
			if err == nil {
				jg.CanonicalText = fact.NormalizedFactText
			}
		}

		output.Groups = append(output.Groups, jg)
	}

	jsonBytes, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	fmt.Println(string(jsonBytes))
	return nil
}

func outputCheckResultsText(result *core.DedupeResult, storer *storage.PostgresStorer, ctx context.Context) {
	fmt.Println()
	fmt.Println("=== DUPLICATE CHECK RESULTS ===")
	fmt.Printf("Total facts checked:     %d\n", result.TotalFactsChecked)
	fmt.Printf("Potential duplicates:    %d\n", result.AutoDeduped)
	fmt.Printf("Duplicate groups found:  %d\n", len(result.DuplicateGroups))

	if len(result.DuplicateGroups) == 0 {
		fmt.Println("\nNo potential duplicates found.")
		return
	}

	fmt.Println("\n--- Potential Duplicates ---")
	for i, group := range result.DuplicateGroups {
		if i >= 50 {
			fmt.Printf("\n... and %d more groups (use --output json for full list)\n", len(result.DuplicateGroups)-50)
			break
		}

		fmt.Printf("\n[%d] Canonical ID: %d", i+1, group.CanonicalID)
		if group.Similarity > 0 {
			fmt.Printf(" (%.1f%% similar)", group.Similarity*100)
		}
		fmt.Println()

		if checkShowDetails {
			// Fetch and display fact text
			canonicalFact, err := storer.GetFactByID(ctx, group.CanonicalID)
			if err == nil {
				fmt.Printf("    Canonical: %s\n", truncateText(canonicalFact.NormalizedFactText, 100))
			}

			for _, dupID := range group.DuplicateIDs {
				dupFact, err := storer.GetFactByID(ctx, dupID)
				if err == nil {
					fmt.Printf("    Duplicate [%d]: %s\n", dupID, truncateText(dupFact.NormalizedFactText, 100))
				}
			}
		} else {
			fmt.Printf("    Duplicate IDs: %v\n", group.DuplicateIDs)
		}
		fmt.Printf("    Reason: %s\n", group.Reason)
	}

	fmt.Println()
	fmt.Println("To mark these as duplicates, run: ./dm dedupe run")
	fmt.Println("Use --target to process a specific person")
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
