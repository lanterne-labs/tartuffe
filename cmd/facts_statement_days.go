package cmd

import (
	"context"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"digital-mirror/backend"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/statementday"
)

// factsStatementDaysCmd finds on the web the exact day of the statements
// known only to the year or the month (backend/statementday): the site then
// shows "8 avril 2013" where it showed "2013". Two LLM calls and two web
// searches per fact; a fact searched once is not searched again.
var factsStatementDaysCmd = &cobra.Command{
	Use:         "statement-days",
	Short:       "Find on the web the exact day of the statements dated to a year or a month (the candidates' by default)",
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsStatementDays,
}

func init() {
	factsCmd.AddCommand(factsStatementDaysCmd)
	factsStatementDaysCmd.Flags().Bool("apply", false, "write the days found (default: report only)")
	factsStatementDaysCmd.Flags().Bool("all", false, "every person's facts, not only the candidates'")
	factsStatementDaysCmd.Flags().Bool("again", false, "also the facts an earlier run found no day for")
	factsStatementDaysCmd.Flags().Int("limit", 0, "at most this many facts (0: all; a dry run defaults to 20)")
}

func runFactsStatementDays(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")
	all, _ := cmd.Flags().GetBool("all")
	again, _ := cmd.Flags().GetBool("again")
	limit, _ := cmd.Flags().GetInt("limit")
	if !apply && limit == 0 {
		limit = 20
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	finder, err := statementday.NewFinderFromEnv()
	if err != nil {
		return fmt.Errorf("statement day finder: %w", err)
	}
	res, err := statementday.Run(ctx, storer, finder, statementday.Options{
		Limit: limit, Apply: apply, Everyone: all, Again: again,
		Report: func(p statementday.Pending, f statementday.Found) {
			day := backend.FormatFactDateISO(f.Day)
			if day == "" {
				day = "-"
			}
			fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\t%s\n", p.FactID, p.Person, backend.FormatFactDateISO(&p.Known), day, f.URL, f.Evidence, f.Reasoning)
		},
	})
	mode := "dry run"
	if apply {
		mode = "written"
	}
	fmt.Printf("%d facts searched: %d days found, %d left at their year or month; %d failed (%s)\n",
		res.Searched, res.Found, res.Searched-res.Found, res.Failed, mode)
	return err
}
