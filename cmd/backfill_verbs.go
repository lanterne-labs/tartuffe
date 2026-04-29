package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var backfillVerbsDryRun bool

var backfillVerbsCmd = &cobra.Command{
	Use:   "backfill-verbs",
	Short: "Backfill canonical_verb from existing embedding_text",
	Long: `Parses the canonical verb prefix from existing embedding_text values,
sets the canonical_verb column, strips the verb from embedding_text,
and regenerates embeddings from the verb-free text.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runBackfillVerbs,
}

func init() {
	factsCmd.AddCommand(backfillVerbsCmd)
	backfillVerbsCmd.Flags().BoolVar(&backfillVerbsDryRun, "dry-run", false, "Show changes without writing to database")
}

func runBackfillVerbs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	// Query all facts where canonical_verb IS NULL
	rows, err := storer.Pool().Query(ctx, `
		SELECT id, COALESCE(embedding_text, '')
		FROM facts
		WHERE canonical_verb IS NULL OR canonical_verb = ''
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("failed to query facts: %w", err)
	}
	defer rows.Close()

	type factToUpdate struct {
		ID            int64
		OldEmbedding  string
		CanonicalVerb string
		NewEmbedding  string
	}

	var updates []factToUpdate
	for rows.Next() {
		var id int64
		var embText string
		if err := rows.Scan(&id, &embText); err != nil {
			return fmt.Errorf("scan error: %w", err)
		}

		verb, topic, ok := categorizer.ParseVerbFromEmbeddingText(embText)
		if !ok {
			log.Debug().Int64("id", id).Str("embedding_text", embText).Msg("No verb prefix found, skipping")
			continue
		}

		updates = append(updates, factToUpdate{
			ID:            id,
			OldEmbedding:  embText,
			CanonicalVerb: verb,
			NewEmbedding:  topic,
		})
	}
	if rows.Err() != nil {
		return fmt.Errorf("rows error: %w", rows.Err())
	}

	log.Info().Int("total", len(updates)).Bool("dry_run", backfillVerbsDryRun).Msg("Facts to backfill")

	if len(updates) == 0 {
		log.Info().Msg("No facts need backfilling")
		return nil
	}

	// Show first few changes
	showCount := 10
	if len(updates) < showCount {
		showCount = len(updates)
	}
	for _, u := range updates[:showCount] {
		log.Info().
			Int64("id", u.ID).
			Str("verb", u.CanonicalVerb).
			Str("old", u.OldEmbedding).
			Str("new", u.NewEmbedding).
			Msg("Backfill preview")
	}
	if len(updates) > showCount {
		log.Info().Int("remaining", len(updates)-showCount).Msg("... and more")
	}

	if backfillVerbsDryRun {
		log.Info().Msg("Dry run complete, no changes made")
		return nil
	}

	// Initialize embedding generator for re-embedding
	embGen, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to init embedding generator: %w", err)
	}

	// Process in batches of 50
	batchSize := 50
	updated := 0

	for i := 0; i < len(updates); i += batchSize {
		end := i + batchSize
		if end > len(updates) {
			end = len(updates)
		}
		batch := updates[i:end]

		// Collect new embedding texts for batch embedding
		texts := make([]string, len(batch))
		for j, u := range batch {
			texts[j] = u.NewEmbedding
		}

		// Generate embeddings
		embeddings, err := embGen.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
		if err != nil {
			log.Error().Err(err).Int("batch_start", i).Msg("Failed to generate embeddings for batch, skipping")
			continue
		}

		if len(embeddings) != len(batch) {
			log.Error().Int("expected", len(batch)).Int("got", len(embeddings)).Msg("Embedding count mismatch")
			continue
		}

		// Update each fact in the batch
		for j, u := range batch {
			_, err := storer.Pool().Exec(ctx, `
				UPDATE facts
				SET canonical_verb = $1, embedding_text = $2, embedding = $3
				WHERE id = $4`,
				u.CanonicalVerb, u.NewEmbedding, embeddings[j], u.ID)
			if err != nil {
				log.Error().Err(err).Int64("id", u.ID).Msg("Failed to update fact")
				continue
			}
			updated++
		}

		log.Info().
			Int("batch", (i/batchSize)+1).
			Int("updated", updated).
			Int("total", len(updates)).
			Msg("Backfill progress")
	}

	log.Info().Int("updated", updated).Int("total", len(updates)).Msg("Backfill complete")
	return nil
}
