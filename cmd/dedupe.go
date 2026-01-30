package cmd

import (
	"github.com/spf13/cobra"
)

// dedupeCmd represents the dedupe command group
var dedupeCmd = &cobra.Command{
	Use:   "dedupe",
	Short: "Commands for fact deduplication",
	Long: `Deduplicate facts in the database using multi-tier similarity detection.

The deduplication system uses:
- Tier 1: Auto-dedupe for high similarity (>92%) using embeddings
- Tier 2: Text similarity pre-filtering for candidates
- Tier 3: LLM verification for uncertain cases (75-92% similarity)
- Tier 4: Keep separate for low similarity (<75%)

Examples:
  # Check for potential duplicates without changes
  ./dm dedupe check

  # Run deduplication on all facts
  ./dm dedupe run

  # Dedupe facts for a specific person
  ./dm dedupe run --target "Marine Le Pen"

  # Dry run - show what would be deduped
  ./dm dedupe run --dry-run`,
}

func init() {
	rootCmd.AddCommand(dedupeCmd)
}
