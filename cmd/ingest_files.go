package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
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
	// The files hold LLM-extracted free text (the news cache): a fact needs a
	// quote, and it can be grouped like a news fact.
	core.RequireQuoteForSourceType("file")

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
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Resolve target name
	resolvedTarget, err := resolveIngestTarget(ctx, storer, targetName, targetType, force)
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
	stanceMgr, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	factInserter := core.NewFactInserter(storer, stanceMgr)
	factInserter.EnableDedupe(storer)

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
		if result.FactsSkipped > 0 {
			fmt.Printf("  Facts skipped: %d\n", result.FactsSkipped)
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
		recordJobMetrics(ctx, storer, jobMetrics("ingest-files", resolvedTarget, map[string]float64{
			"files_processed": float64(result.FilesProcessed),
			"facts_extracted": float64(result.FactsExtracted),
			"facts_stored":    float64(result.FactsStored),
			"facts_skipped":   float64(result.FactsSkipped),
		}))
	}

	return nil
}

// resolveIngestTarget resolves --target to the stored name of an existing
// person/party, or creates it: --force takes a confident fuzzy match and
// creates as --target-type without asking, otherwise the user picks among
// the candidates or confirms the creation.
func resolveIngestTarget(ctx context.Context, storer *storage.PostgresStorer, targetName string, targetType string, force bool) (string, error) {
	if force {
		entityType := "human"
		if targetType == "party" {
			entityType = "party"
		}
		person, err := resolveOrCreatePerson(ctx, storer, targetName, entityType)
		if err != nil {
			return "", err
		}
		return person.Name, nil
	}
	person, ok, err := resolvePersonInteractive(ctx, storer, targetName, true)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("target %q not found and not created", targetName)
	}
	return person.Name, nil
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
