package cmd

import (
	"fmt"

	"digital-mirror/backend/eval"

	"github.com/spf13/cobra"
)

var evalValidateCasesPath string

var evalValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the eval YAML file without touching the database.",
	Long: `Reads the corpus, checks every fact has the mandatory fields, every
expected_pair references known fact IDs, dates parse, and there are no
duplicate IDs. Prints a summary of the corpus shape. No DB required —
safe to run anywhere as a quick lint.`,
	RunE: runEvalValidate,
}

func init() {
	evalCmd.AddCommand(evalValidateCmd)
	evalValidateCmd.Flags().StringVar(&evalValidateCasesPath, "cases", "eval/cases.yaml", "Path to the eval YAML file")
}

func runEvalValidate(cmd *cobra.Command, args []string) error {
	corpus, err := eval.LoadCorpus(evalValidateCasesPath)
	if err != nil {
		return err
	}

	allFacts := corpus.AllFacts()
	yes, no := 0, 0
	for _, p := range corpus.ExpectedPairs {
		if p.IsContradiction() {
			yes++
		} else {
			no++
		}
	}

	fmt.Printf("Corpus OK: %s\n", evalValidateCasesPath)
	fmt.Printf("  persons:        %d\n", len(corpus.Persons))
	fmt.Printf("  total facts:    %d\n", len(allFacts))
	fmt.Printf("  expected pairs: %d  (YES=%d, NO=%d)\n", len(corpus.ExpectedPairs), yes, no)
	for _, p := range corpus.Persons {
		factCount := 0
		for _, s := range p.Stances {
			factCount += len(s.Facts)
		}
		fmt.Printf("  • %-20s  %d stances, %d facts\n", p.Name, len(p.Stances), factCount)
	}
	return nil
}
