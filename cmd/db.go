package cmd

import (
	"github.com/spf13/cobra"
)

// dbCmd represents the base command for database related operations.
var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Manage the application database.",
	Long:  `Provides subcommands to manage the application database, such as resetting tables.`,
}

func init() {
	rootCmd.AddCommand(dbCmd)
}
