package cmd

import "github.com/spf13/cobra"

// pipelineCmd is the parent group for pipeline-related CLI commands. Subcommands
// live in their own files (e.g. cmd/pipeline_daily.go).
var pipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Commands for the daily ingestion pipeline.",
	Long:  `Manually trigger the daily pipeline that's normally driven by the host cron.`,
}

func init() {
	rootCmd.AddCommand(pipelineCmd)
}
