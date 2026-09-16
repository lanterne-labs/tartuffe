package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/embedding"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	embeddingsRegenerateBatchSize int
	embeddingsRegenerateDryRun    bool
)

var embeddingsRegenerateCmd = &cobra.Command{
	Use:   "regenerate",
	Short: "Generate embeddings for facts where embedding IS NULL",
	Long: `Walks every fact whose embedding column is NULL and refills it via the
configured OPEN_AI_API embeddings endpoint. Uses COALESCE(NULLIF(embedding_text, ''), fact_text)
as the source string, matching the ingestion pipeline.

Idempotent and resumable: re-running picks up where it left off. Safe to kill
mid-run (each batch commits per-row).

Run after 'dm embeddings invalidate' to repopulate vectors after a model swap.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runEmbeddingsRegenerate,
}

func init() {
	embeddingsCmd.AddCommand(embeddingsRegenerateCmd)
	embeddingsRegenerateCmd.Flags().IntVar(&embeddingsRegenerateBatchSize, "batch-size", 50, "Number of facts to embed per request")
	embeddingsRegenerateCmd.Flags().BoolVar(&embeddingsRegenerateDryRun, "dry-run", false, "Show how many facts would be processed without calling the embedding API")
}

func runEmbeddingsRegenerate(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	if embeddingsRegenerateBatchSize <= 0 {
		return fmt.Errorf("--batch-size must be > 0")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	total, err := storer.CountFactsWithoutEmbedding(ctx)
	if err != nil {
		return err
	}

	log.Info().Int64("facts_pending", total).Int("batch_size", embeddingsRegenerateBatchSize).Bool("dry_run", embeddingsRegenerateDryRun).Msg("Starting embedding regeneration")

	if total == 0 {
		fmt.Println("No facts with NULL embedding. Nothing to do.")
		return nil
	}

	if embeddingsRegenerateDryRun {
		fmt.Printf("[DRY-RUN] Would regenerate embeddings for %d facts in batches of %d\n", total, embeddingsRegenerateBatchSize)
		return nil
	}

	embGen, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("init embedding generator: %w", err)
	}

	stats, err := core.RegenerateEmbeddings(ctx, storer, embGen, embeddingsRegenerateBatchSize)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("=== REGENERATE COMPLETE ===")
	fmt.Printf("Embeddings written:        %d\n", stats.Processed)
	fmt.Printf("Failures:                  %d\n", stats.Failed)
	fmt.Println()
	fmt.Println("Next step: ./dm stances rebuild --all")

	return nil
}
