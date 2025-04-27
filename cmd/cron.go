package cmd

import (
	"github.com/spf13/cobra"
)

// cronCmd represents the cron command group
var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Commands related to scheduled/cron jobs.",
	Long:  `Manage and trigger cron jobs for the fact extraction pipeline.`,
	// Run: func(cmd *cobra.Command, args []string) { // Optional: action for 'digital-mirror cron'
	// 	fmt.Println("Use a subcommand like 'run'")
	// },
}

func init() {
	rootCmd.AddCommand(cronCmd)

	// Here you can define flags specific to the cron command group
	// e.g., cronCmd.PersistentFlags().String("foo", "", "A help for foo")
} 