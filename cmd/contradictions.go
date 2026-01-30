package cmd

import (
	"github.com/spf13/cobra"
)

// contradictionsCmd represents the contradictions command group
var contradictionsCmd = &cobra.Command{
	Use:   "contradictions",
	Short: "Commands related to contradiction detection.",
	Long:  `Detect and manage contradictions between stored facts.`,
}

func init() {
	rootCmd.AddCommand(contradictionsCmd)
}
