package cmd

import (
	"github.com/spf13/cobra"
)

var scrapeCmd = &cobra.Command{
	Use:   "scrape",
	Short: "Scrape content from URLs.",
}

func init() {
	rootCmd.AddCommand(scrapeCmd)
}
