package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/embedding"
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

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	var embedder embedding.EmbeddingGenerator
	if !backfillVerbsDryRun {
		embGen, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to init embedding generator: %w", err)
		}
		embedder = embGen
	}

	_, err = core.BackfillCanonicalVerbs(ctx, storer, embedder, categorizer.ParseVerbFromEmbeddingText, backfillVerbsDryRun)
	return err
}
