package cmd

import "github.com/spf13/cobra"

var translateCmd = &cobra.Command{
	Use:   "translate",
	Short: "Translation subsystem: fill multi-language translations for facts, stances and contradictions.",
}

func init() {
	rootCmd.AddCommand(translateCmd)
}
