package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// ingestFilesCmd represents the ingest files command
var ingestFilesCmd = &cobra.Command{
	Use:         "files",
	Short:       "Ingest facts from local JSON files",
	Annotations: map[string]string{"needs_db": "true"},
	Long: `Ingest facts from local JSON files into the database.

Files must be in the cached content JSON format:
{
  "url": "https://example.com/article",
  "title": "Article Title",
  "content": "Full text content...",
  "date_published": "2026-01-15T10:30:00Z"
}

Examples:
  # Ingest all JSON files from a directory
  ./dm ingest files --dir ./scraped_content/news_cache --target "Boris Vallaud"

  # Ingest for a party
  ./dm ingest files --dir ./party_docs --target "Socialistes" --target-type party

  # Dry run (extract facts but don't store)
  ./dm ingest files --dir ./articles --target "Marine Le Pen" --dry-run

  # Single file
  ./dm ingest files --file ./article.json --target "Emmanuel Macron"`,
	RunE: runIngestFiles,
}

func init() {
	ingestCmd.AddCommand(ingestFilesCmd)

	// Source selection (either --dir or --file required)
	ingestFilesCmd.Flags().String("dir", "", "directory containing JSON files to process")
	ingestFilesCmd.Flags().String("file", "", "single JSON file to process")

	// Target configuration
	ingestFilesCmd.Flags().String("target", "", "person or party name to associate facts with (required)")
	ingestFilesCmd.Flags().String("target-type", "person", "target type: person or party")
	ingestFilesCmd.MarkFlagRequired("target")

	// Processing options
	ingestFilesCmd.Flags().String("source-type", "file", "source type for fact_sources table")
	ingestFilesCmd.Flags().Bool("dry-run", false, "extract facts but don't store them")
	ingestFilesCmd.Flags().BoolP("force", "f", false, "create target if not found in database")
	ingestFilesCmd.Flags().String("keep-source", "", "dump article content + extracted facts to this directory for debugging")
}

func runIngestFiles(cmd *cobra.Command, args []string) error {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("No .env file found, relying on environment variables")
	}

	ctx := context.Background()

	// Parse flags
	dir, _ := cmd.Flags().GetString("dir")
	file, _ := cmd.Flags().GetString("file")
	targetName, _ := cmd.Flags().GetString("target")
	targetType, _ := cmd.Flags().GetString("target-type")
	sourceType, _ := cmd.Flags().GetString("source-type")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	keepSourceDir, _ := cmd.Flags().GetString("keep-source")

	// Validate source selection
	if dir == "" && file == "" {
		return fmt.Errorf("either --dir or --file is required")
	}
	if dir != "" && file != "" {
		return fmt.Errorf("cannot specify both --dir and --file")
	}

	// Validate target type
	if targetType != "person" && targetType != "party" {
		return fmt.Errorf("--target-type must be 'person' or 'party', got: %s", targetType)
	}

	// Initialize database connection
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Resolve target name
	resolvedTarget, err := resolveTarget(ctx, storer, targetName, targetType, force)
	if err != nil {
		return err
	}

	log.Info().
		Str("target", resolvedTarget).
		Str("type", targetType).
		Bool("dry_run", dryRun).
		Msg("Starting file ingestion")

	// Initialize fact extractor
	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize fact extractor: %w", err)
	}

	// Initialize embedder
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedder: %w", err)
	}

	// Initialize stance manager (mandatory) and fact inserter
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	factInserter := core.NewFactInserter(storer, stanceMgr)

	// Create file ingester
	ingester := core.NewFileIngester(factExtractor, embedder, factInserter)

	// Set up options
	opts := core.IngestOptions{
		TargetName:    resolvedTarget,
		TargetType:    targetType,
		SourceType:    sourceType,
		DryRun:        dryRun,
		KeepSourceDir: keepSourceDir,
	}

	// Process files
	var result *core.IngestResult
	if dir != "" {
		// Check for target-specific subdirectory
		targetDir := resolveTargetDirectory(dir, resolvedTarget)
		log.Info().Str("dir", targetDir).Msg("Using cache directory")
		result, err = ingester.IngestDirectory(ctx, targetDir, opts)
	} else {
		result, err = ingester.IngestFiles(ctx, []string{file}, opts)
	}

	if err != nil {
		return fmt.Errorf("ingestion failed: %w", err)
	}

	// Print summary
	fmt.Println()
	fmt.Println("Ingestion complete:")
	fmt.Printf("  Files processed:    %d\n", result.FilesProcessed)
	fmt.Printf("  Facts extracted:    %d\n", result.FactsExtracted)
	if !dryRun {
		fmt.Printf("  Facts stored:       %d\n", result.FactsStored)
		if result.DuplicatesSkipped > 0 {
			fmt.Printf("  Duplicates skipped: %d\n", result.DuplicatesSkipped)
		}
	} else {
		fmt.Println("  [DRY-RUN] No facts were stored")
	}
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:             %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("    - %s: %v\n", e.Path, e.Error)
		}
	}

	// Record job metrics (skip in dry-run mode)
	if !dryRun {
		jobMetrics := []storage.JobMetric{
			{JobType: "ingest-files", Target: resolvedTarget, Metric: "files_processed", Value: float64(result.FilesProcessed)},
			{JobType: "ingest-files", Target: resolvedTarget, Metric: "facts_extracted", Value: float64(result.FactsExtracted)},
			{JobType: "ingest-files", Target: resolvedTarget, Metric: "facts_stored", Value: float64(result.FactsStored)},
			{JobType: "ingest-files", Target: resolvedTarget, Metric: "duplicates_skipped", Value: float64(result.DuplicatesSkipped)},
		}
		if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
			log.Error().Err(err).Msg("Failed to record job metrics")
		}
	}

	return nil
}

// resolveTarget resolves the target name to an existing person/party or creates one.
func resolveTarget(ctx context.Context, storer *storage.PostgresStorer, targetName string, targetType string, force bool) (string, error) {
	// Use fuzzy matching to resolve the target
	result := core.ResolveTargetName(ctx, storer, targetName)

	if result.Error != nil {
		return "", fmt.Errorf("failed to resolve target name: %w", result.Error)
	}

	// Exact match found
	if result.IsExactMatch {
		log.Info().
			Str("query", targetName).
			Str("matched", result.ExactMatch.Name).
			Msg("Exact match found")
		return result.ExactMatch.Name, nil
	}

	// Fuzzy matches found - needs disambiguation
	if len(result.FuzzyMatches) > 0 {
		if force {
			// In force mode with fuzzy matches, use top match if high confidence
			topMatch := result.FuzzyMatches[0]
			if topMatch.Score >= 0.8 {
				log.Info().
					Str("query", targetName).
					Str("matched", topMatch.Target.Name).
					Float64("score", topMatch.Score).
					Msg("Using high-confidence fuzzy match (force mode)")
				return topMatch.Target.Name, nil
			}
			// Low confidence - create new
			log.Info().
				Str("query", targetName).
				Float64("top_score", topMatch.Score).
				Msg("Low-confidence matches, creating new target (force mode)")
			return createTarget(ctx, storer, targetName, targetType)
		}

		// Interactive disambiguation
		fmt.Printf("Query: %q\n", targetName)
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.EntityType, match.Score)
		}
		fmt.Printf("  0) Create new %s %q\n", targetType, targetName)
		fmt.Printf("  -1) Cancel\n")
		fmt.Print("Enter your choice: ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil {
			return "", fmt.Errorf("invalid choice")
		}

		if choice == -1 {
			return "", fmt.Errorf("cancelled by user")
		}

		if choice == 0 {
			return createTarget(ctx, storer, targetName, targetType)
		}

		if choice < 1 || choice > len(result.FuzzyMatches) {
			return "", fmt.Errorf("invalid choice: %d", choice)
		}

		selected := result.FuzzyMatches[choice-1]
		return selected.Target.Name, nil
	}

	// No matches found
	if force {
		// In force mode, create the target
		log.Info().Str("name", targetName).Str("type", targetType).Msg("Creating target (force mode)")
		return createTarget(ctx, storer, targetName, targetType)
	}

	// Interactive mode - ask user
	fmt.Printf("Target %q not found in database.\n", targetName)
	fmt.Printf("Create as %s? (y/n): ", targetType)

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "y" || input == "yes" {
		return createTarget(ctx, storer, targetName, targetType)
	}

	return "", fmt.Errorf("target %q not found and not created", targetName)
}

// resolveTargetDirectory determines the cache directory for a target.
// It checks for a target-specific subdirectory (e.g., "marine_le_pen") and uses it if it exists.
// Otherwise, it falls back to the base directory.
func resolveTargetDirectory(baseDir string, targetName string) string {
	// Normalize target name to directory-safe string
	normalizedName := util.NormalizeToDirName(targetName)

	// Check if target-specific subdirectory exists
	targetDir := filepath.Join(baseDir, normalizedName)
	if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
		log.Debug().
			Str("target", targetName).
			Str("normalized", normalizedName).
			Str("dir", targetDir).
			Msg("Found target-specific subdirectory")
		return targetDir
	}

	// Fall back to base directory
	log.Debug().
		Str("target", targetName).
		Str("base_dir", baseDir).
		Msg("No target-specific subdirectory found, using base directory")
	return baseDir
}

// createTarget creates a new person or party in the database.
func createTarget(ctx context.Context, storer *storage.PostgresStorer, name string, targetType string) (string, error) {
	entityType := "human"
	if targetType == "party" {
		entityType = "party"
	}

	id, inserted, err := storer.AddPersonWithType(ctx, name, entityType)
	if err != nil {
		return "", fmt.Errorf("failed to create %s: %w", targetType, err)
	}

	if inserted {
		log.Info().Str("name", name).Int64("id", id).Str("type", entityType).Msg("Created new target")
		fmt.Printf("Created new %s: %s (ID: %d)\n", targetType, name, id)
	} else {
		log.Info().Str("name", name).Int64("id", id).Msg("Target already exists")
	}

	return name, nil
}
