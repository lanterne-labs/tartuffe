package cmd

import (
	"github.com/spf13/cobra"
)

// ingestCmd represents the parent ingest command
var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Ingest facts from various sources",
	Long:  `Ingest facts from local files, URLs, or other sources into the database.`,
}

func init() {
	rootCmd.AddCommand(ingestCmd)
}
