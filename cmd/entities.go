package cmd

import "github.com/spf13/cobra"

// entitiesCmd is the parent group for entity-validation commands. Subcommands:
//
//	entities validate   — run the same validator the extractor uses against every
//	                      existing persons row, surface rejections, optionally delete.
//
// The intent is for `entities validate --apply` to be a periodic cleanup task
// (the host cron) that mirrors the pre-insert guard wired into FactInserter, so
// what slips through the live pipeline and what gets weeded out later go
// through identical code (see backend/entityvalidator).
var entitiesCmd = &cobra.Command{
	Use:   "entities",
	Short: "Inspect / clean the persons table through the entity-validator pipeline.",
	Long: `Run the same wiki+LLM validator that gates new fact-insertions against
existing rows in the persons table.`,
}

func init() {
	rootCmd.AddCommand(entitiesCmd)
}
