package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/deps"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var embeddingsInvalidateConfirm bool

var embeddingsInvalidateCmd = &cobra.Command{
	Use:   "invalidate",
	Short: "Wipe all fact embeddings and stance documents",
	Long: `Sets facts.embedding = NULL for every row and deletes every stance_document
in a single transaction. Used when swapping the embedding model: the stored
vectors live in a different vector space than the new model, so they must be
discarded before re-embedding.

Run 'dm embeddings regenerate' afterwards to refill the vectors, then
'dm stances rebuild --all' to rebuild the stance documents.

This operation is destructive and irreversible without re-running regenerate.
Take a database snapshot before running.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runEmbeddingsInvalidate,
}

func init() {
	embeddingsCmd.AddCommand(embeddingsInvalidateCmd)
	embeddingsInvalidateCmd.Flags().BoolVar(&embeddingsInvalidateConfirm, "confirm", false, "Required to actually run the wipe (without it, this command exits non-zero)")
}

func runEmbeddingsInvalidate(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	factCount, embeddedCount, stanceCount, err := storer.CountEmbeddingInvalidationPlan(ctx)
	if err != nil {
		return err
	}

	fmt.Println("=== EMBEDDING INVALIDATION PLAN ===")
	fmt.Printf("Total facts:               %d\n", factCount)
	fmt.Printf("Facts with embeddings:     %d  (will be set to NULL)\n", embeddedCount)
	fmt.Printf("Stance documents:          %d  (will be deleted, with cascading stance_document_facts)\n", stanceCount)
	fmt.Println()

	if !embeddingsInvalidateConfirm {
		fmt.Println("Refusing to run without --confirm. Pass --confirm to enable the destructive path.")
		return fmt.Errorf("--confirm flag required")
	}

	fmt.Print("Type 'yes' to proceed with the wipe: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		fmt.Println("Cancelled")
		return nil
	}

	nulled, deleted, err := storer.InvalidateEmbeddings(ctx)
	if err != nil {
		return err
	}

	log.Info().
		Int64("embeddings_nulled", nulled).
		Int64("stance_documents_deleted", deleted).
		Msg("Embeddings invalidated")

	fmt.Println()
	fmt.Println("=== INVALIDATION COMPLETE ===")
	fmt.Printf("Embeddings nulled:         %d\n", nulled)
	fmt.Printf("Stance documents deleted:  %d\n", deleted)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1) ./dm embeddings regenerate")
	fmt.Println("  2) ./dm stances rebuild --all")

	return nil
}
