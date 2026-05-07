package cmd

import "github.com/spf13/cobra"

// complaintsCmd is the parent group for user-complaint management commands.
// Complaints are filed via a Tally form on the frontend; this CLI lets you
// triage them with an LLM and apply (or preview) the verdicts.
var complaintsCmd = &cobra.Command{
	Use:   "complaints",
	Short: "Manage user-submitted complaints about facts, contradictions, or stances.",
	Long: `Visitors can flag a fact, contradiction, or stance as wrong via a Tally form.
This command group fetches those complaints and lets an LLM judge each one,
deleting the offending row, dismissing wrong reports, or keeping ambiguous ones.`,
}

func init() {
	rootCmd.AddCommand(complaintsCmd)
}
