package cmd

import (
	"github.com/spf13/cobra"
)

// personsCmd represents the persons command group
var personsCmd = &cobra.Command{
	Use:   "persons",
	Short: "Commands related to managing persons in the database.",
	Long:  `Add, list, or remove persons tracked by the fact extraction pipeline.`,
}

func init() {
	rootCmd.AddCommand(personsCmd)
	// Add flags relevant to all person commands here if needed
}
