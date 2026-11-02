package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
)

var embeddingsPruneApply bool

var embeddingsPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Clear the embeddings of the facts that name no candidate (dry run unless --apply)",
	Long: `Only the candidates' facts are compared (grouping, stances, contradiction
detection), so only they need an embedding. This clears the others' and says
how many. It can be undone: once a person becomes a candidate,
dm embeddings regenerate embeds their facts again. Run VACUUM FULL facts
afterwards to give the space back to the disk.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		storer, err := deps.OpenStorer(cmd.Context())
		if err != nil {
			return err
		}
		defer storer.Close()
		n, err := storer.PruneOtherEmbeddings(cmd.Context(), embeddingsPruneApply)
		if err != nil {
			return err
		}
		if embeddingsPruneApply {
			fmt.Printf("cleared the embeddings of %d facts that name no candidate\n", n)
		} else {
			fmt.Printf("%d facts name no candidate and carry an embedding (dry run; --apply clears them)\n", n)
		}
		return nil
	},
}

func init() {
	embeddingsCmd.AddCommand(embeddingsPruneCmd)
	embeddingsPruneCmd.Flags().BoolVar(&embeddingsPruneApply, "apply", false, "Clear them (default: count only)")
}
