package cmd

import (
	"context"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	pool := storer.Pool()

	var total int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts WHERE embedding IS NULL`).Scan(&total); err != nil {
		return fmt.Errorf("count facts to regenerate: %w", err)
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

	processed := int64(0)
	failed := int64(0)
	batchNum := 0

	for {
		rows, err := pool.Query(ctx, `
			SELECT id, COALESCE(NULLIF(embedding_text, ''), fact_text)
			FROM facts
			WHERE embedding IS NULL
			ORDER BY id
			LIMIT $1`, embeddingsRegenerateBatchSize)
		if err != nil {
			return fmt.Errorf("query batch: %w", err)
		}

		var ids []int64
		var texts []string
		for rows.Next() {
			var id int64
			var text string
			if err := rows.Scan(&id, &text); err != nil {
				rows.Close()
				return fmt.Errorf("scan row: %w", err)
			}
			ids = append(ids, id)
			texts = append(texts, text)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate rows: %w", err)
		}

		if len(ids) == 0 {
			break
		}

		batchNum++

		vecs, err := embGen.GenerateEmbeddings(ctx, texts)
		if err != nil {
			log.Error().Err(err).Int("batch", batchNum).Int("size", len(ids)).Msg("Failed to generate embeddings for batch, aborting")
			return fmt.Errorf("generate embeddings (batch %d): %w", batchNum, err)
		}
		if len(vecs) != len(ids) {
			return fmt.Errorf("embedding count mismatch: expected %d, got %d", len(ids), len(vecs))
		}

		for i, id := range ids {
			if err := storer.UpdateFactEmbedding(ctx, id, vecs[i]); err != nil {
				log.Error().Err(err).Int64("fact_id", id).Msg("Failed to update fact embedding")
				failed++
				continue
			}
			processed++
		}

		pct := float64(processed) / float64(total) * 100
		log.Info().
			Int("batch", batchNum).
			Int64("processed", processed).
			Int64("failed", failed).
			Int64("total", total).
			Float64("pct", pct).
			Msg("Regenerate progress")
	}

	fmt.Println()
	fmt.Println("=== REGENERATE COMPLETE ===")
	fmt.Printf("Embeddings written:        %d\n", processed)
	fmt.Printf("Failures:                  %d\n", failed)
	fmt.Println()
	fmt.Println("Next step: ./dm stances rebuild --all")

	return nil
}
