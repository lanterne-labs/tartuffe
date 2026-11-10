package cmd

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"encoding/json"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	explainPersonFlag string
	explainIDsFlag    []int64
	explainAllFlag    bool
	explainLimitFlag  int
	explainDryRunFlag bool
)

var contradictionsExplainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Write the explanation the site shows with each contradiction.",
	Long: `Writes the explanation of the contradictions that have none, or one written
from other facts or an older prompt: a short text for a reader who has only the
two facts, made from the facts, the quotes of their sources and their dates.
The maintenance pass does the same, 500 at a time; this command is for a
backfill or a look at what the prompt gives.

Without a flag it takes the candidates' contradictions, the newest first.
Each explanation is printed as a JSON line (the two facts, the judge's reason,
the explanation, and the doubt the writer has about the pair after reading the
quotes, if any): the lines with a doubt are the contradictions to review.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionExplain,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsExplainCmd)
	contradictionsExplainCmd.Flags().StringVarP(&explainPersonFlag, "person", "p", "", "Only this person's contradictions (default: the candidates')")
	contradictionsExplainCmd.Flags().Int64SliceVar(&explainIDsFlag, "id", nil, "Only these contradictions (ids, comma-separated)")
	contradictionsExplainCmd.Flags().BoolVar(&explainAllFlag, "all", false, "Also write again the explanations that are up to date")
	contradictionsExplainCmd.Flags().IntVar(&explainLimitFlag, "limit", 0, "At most this many contradictions (0: no limit)")
	contradictionsExplainCmd.Flags().BoolVar(&explainDryRunFlag, "dry-run", false, "Print the explanations without storing them")
}

// explainedLine is one line of the command's output.
type explainedLine struct {
	ID          int64  `json:"id"`
	Person      string `json:"person"`
	FactA       string `json:"fact_a"`
	QuoteA      string `json:"quote_a,omitempty"`
	FactB       string `json:"fact_b"`
	QuoteB      string `json:"quote_b,omitempty"`
	Reason      string `json:"reason"`
	Explanation string `json:"explanation"`
	Doubt       string `json:"doubt,omitempty"`
}

func runContradictionExplain(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	opts := contradiction.ExplainOptions{
		ExplainFilter: storage.ExplainFilter{IDs: explainIDsFlag, All: explainAllFlag, Limit: explainLimitFlag},
		DryRun:        explainDryRunFlag,
	}
	if explainPersonFlag != "" {
		person, err := core.ResolvePersonID(ctx, storer, explainPersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		opts.PersonID = person.ID
	}
	out := json.NewEncoder(os.Stdout)
	out.SetEscapeHTML(false)
	side := func(s storage.ExplainSide) string {
		return fmt.Sprintf("[%d, %s] %s", s.FactID, backend.FormatFactDate(s.Date), s.Text)
	}
	opts.OnExplained = func(c storage.ContradictionToExplain, e contradiction.Explanation) {
		if err := out.Encode(explainedLine{
			ID: c.ID, Person: c.Person,
			FactA: side(c.A), QuoteA: c.A.Quote, FactB: side(c.B), QuoteB: c.B.Quote,
			Reason: c.Reason, Explanation: e.Text, Doubt: e.Doubt,
		}); err != nil {
			log.Error().Err(err).Int64("contradiction_id", c.ID).Msg("Failed to print an explanation")
		}
	}

	ex, err := contradiction.NewExplainerFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create the explainer: %w", err)
	}
	res, err := contradiction.RunExplain(ctx, storer, ex, opts)
	verb := "written"
	if explainDryRunFlag {
		verb = "drafted (dry run, nothing stored)"
	}
	fmt.Fprintf(os.Stderr, "Explanations: %d to write, %d %s, %d failed, %d with a doubt to review.\n",
		res.Pending, res.Written, verb, res.Failed, len(res.Doubts))
	return err
}
