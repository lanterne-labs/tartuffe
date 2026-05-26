package cmd

import "github.com/spf13/cobra"

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "End-to-end evaluation harness for the contradiction-detection pipeline.",
	Long: `Loads a synthetic test corpus (see eval/cases.yaml), ingests every fact
through the production FactInserter + stance manager, runs contradiction
detection on the test persons, and compares the resulting contradictions
against the corpus's expected_pairs. Reports precision/recall and per-rule
confusion matrix.

Intended for offline evaluation against a local Supabase + a local LLM
gateway. Do NOT run against production — the corpus uses fictional persons
that would pollute real data.`,
}

func init() {
	rootCmd.AddCommand(evalCmd)
}
