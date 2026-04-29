package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/storage"
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	pool := storer.Pool()

	var factCount, embeddedCount, stanceCount int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts`).Scan(&factCount); err != nil {
		return fmt.Errorf("count facts: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts WHERE embedding IS NOT NULL`).Scan(&embeddedCount); err != nil {
		return fmt.Errorf("count embedded facts: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM stance_documents`).Scan(&stanceCount); err != nil {
		return fmt.Errorf("count stance documents: %w", err)
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

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	updateRes, err := tx.Exec(ctx, `UPDATE facts SET embedding = NULL WHERE embedding IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("nulling fact embeddings: %w", err)
	}
	deleteRes, err := tx.Exec(ctx, `DELETE FROM stance_documents`)
	if err != nil {
		return fmt.Errorf("deleting stance_documents: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	log.Info().
		Int64("embeddings_nulled", updateRes.RowsAffected()).
		Int64("stance_documents_deleted", deleteRes.RowsAffected()).
		Msg("Embeddings invalidated")

	fmt.Println()
	fmt.Println("=== INVALIDATION COMPLETE ===")
	fmt.Printf("Embeddings nulled:         %d\n", updateRes.RowsAffected())
	fmt.Printf("Stance documents deleted:  %d\n", deleteRes.RowsAffected())
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1) ./dm embeddings regenerate")
	fmt.Println("  2) ./dm stances rebuild --all")

	return nil
}
