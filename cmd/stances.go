package cmd

import "github.com/spf13/cobra"

var stancesCmd = &cobra.Command{
	Use:   "stances",
	Short: "Commands for stance document management",
	Long: `Manage stance documents that compress facts into topic summaries.

Stance documents reduce contradiction detection cost from O(N) to O(1) LLM calls
per new fact by maintaining compressed summaries per topic per person.

Examples:
  # Backfill stance documents from existing facts
  ./dm stances backfill --person "Marine Le Pen"

  # List stance documents for a person
  ./dm stances list --person "Marine Le Pen"

  # Show aggregate stats
  ./dm stances stats

  # Rebuild stances from underlying facts
  ./dm stances rebuild --person "Marine Le Pen"`,
}

func init() {
	rootCmd.AddCommand(stancesCmd)
}
