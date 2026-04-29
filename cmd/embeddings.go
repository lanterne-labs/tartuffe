package cmd

import "github.com/spf13/cobra"

var embeddingsCmd = &cobra.Command{
	Use:   "embeddings",
	Short: "Commands for managing fact embeddings",
	Long: `Manage the pgvector embeddings stored on facts.embedding.

Used to coordinate one-shot model swaps: invalidate wipes all vectors and the
stance documents derived from them; regenerate refills facts.embedding via the
currently configured OPEN_AI_API endpoint. Run 'dm stances rebuild --all'
afterwards to rebuild stance documents from the new vectors.

Examples:
  # Wipe all embeddings (destructive, asks for confirmation)
  ./dm embeddings invalidate --confirm

  # Re-embed every fact with NULL embedding (resumable)
  ./dm embeddings regenerate --batch-size=50`,
}

func init() {
	rootCmd.AddCommand(embeddingsCmd)
}
