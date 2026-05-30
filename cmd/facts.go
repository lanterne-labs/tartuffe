package cmd

import (
	"github.com/spf13/cobra"
)

// factsCmd represents the facts command group
var factsCmd = &cobra.Command{
	Use:   "facts",
	Short: "Commands related to viewing stored facts.",
	Long:  `Query and display facts stored in the database.`,
}

func init() {
	rootCmd.AddCommand(factsCmd)
	// Add flags relevant to all fact commands here if needed
}
