package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	detectPersonFlag  string
	detectExcludeFlag []string
	detectDryRun      bool
	detectCheckAll    bool
	detectDebug       bool
)

var contradictionsDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Run contradiction detection on existing facts.",
	Long: `Analyzes stored facts to find contradictions using semantic similarity
and LLM-based comparison. By default, processes all persons. Use --person flag
to process only a specific person. Use --dry-run to see what would be detected
without recording to the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionDetection,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsDetectCmd)
	contradictionsDetectCmd.Flags().StringVarP(&detectPersonFlag, "person", "p", "", "Specific person name to process (optional)")
	contradictionsDetectCmd.Flags().StringSliceVar(&detectExcludeFlag, "exclude", nil, "Person names to skip (comma-separated)")
	contradictionsDetectCmd.Flags().BoolVar(&detectDryRun, "dry-run", false, "Show detected contradictions without recording to database")
	contradictionsDetectCmd.Flags().BoolVar(&detectCheckAll, "check-all", false, "Check all fact pairs, not just semantically similar ones")
	contradictionsDetectCmd.Flags().BoolVar(&detectDebug, "debug", false, "Print LLM reasoning and verdict for every pair")
}

// runContradictionDetection is the CLI wrapper that delegates to executeContradictionDetection.
func runContradictionDetection(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	opts := ContradictionDetectOpts{
		Person:   detectPersonFlag,
		Exclude:  detectExcludeFlag,
		DryRun:   detectDryRun,
		CheckAll: detectCheckAll,
		Debug:    detectDebug,
	}

	return executeContradictionDetection(ctx, storer, opts)
}

// ContradictionDetectOpts configures contradiction detection, used by both CLI and pipeline scheduler.
type ContradictionDetectOpts struct {
	Person   string   // specific person name (empty = all)
	Exclude  []string // person names to skip
	DryRun   bool
	CheckAll bool // check all pairs, not just semantically similar
	Debug    bool // print LLM reasoning for every pair
}

// executeContradictionDetection resolves the person names in opts, runs
// contradiction.RunDetection and prints the per-pair and summary output.
func executeContradictionDetection(ctx context.Context, storer *storage.PostgresStorer, opts ContradictionDetectOpts) error {
	detectOpts := contradiction.DetectOptions{
		DryRun:    opts.DryRun,
		CheckAll:  opts.CheckAll,
		OnVerdict: printPairVerdict(opts.Debug),
	}
	if opts.Person != "" {
		person, err := core.ResolvePersonID(ctx, storer, opts.Person, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		log.Info().Str("person", person.Name).Int64("id", person.ID).Msg("Processing single person")
		detectOpts.Persons = []storage.PersonInfo{toPersonInfo(person)}
	}
	for _, name := range opts.Exclude {
		detectOpts.Exclude = append(detectOpts.Exclude, util.NormalizeString(name))
	}

	res, err := contradiction.RunDetection(ctx, storer, detectOpts)
	if err != nil {
		return err
	}

	fmt.Printf("\n=== SUMMARY ===\n")
	if res.PairsChecked == 0 {
		fmt.Printf("No new pairs to check.\n")
		return nil
	}
	fmt.Printf("Total pairs checked: %d\n", res.PairsChecked)
	fmt.Printf("Total contradictions found: %d\n", res.Contradictions)
	if opts.DryRun {
		fmt.Printf("(Dry run - nothing recorded to database)\n")
	}
	return nil
}

// printPairVerdict prints every detected contradiction and, with debug, the
// detector's reasoning and verdict for every judged pair.
func printPairVerdict(debug bool) func(contradiction.PairVerdict) {
	return func(v contradiction.PairVerdict) {
		if debug {
			fmt.Printf("\n[DEBUG] Fact A [%d]: %q\n", v.FactAID, v.FactAText)
			fmt.Printf("[DEBUG] Fact B [%d]: %q\n", v.FactBID, v.FactBText)
			fmt.Printf("[DEBUG] REASONING: %s\n", v.Reasoning)
			verdict := "NO"
			if v.Flagged {
				verdict = "YES"
			}
			fmt.Printf("[DEBUG] VERDICT: %s — %s\n", verdict, v.Reason)
		}
		if v.Contradiction {
			fmt.Printf("\n=== CONTRADICTION DETECTED ===\n")
			fmt.Printf("Person: %s\n", v.Person)
			fmt.Printf("Fact A [%d]: %s\n", v.FactAID, v.FactAText)
			fmt.Printf("Fact B [%d]: %s\n", v.FactBID, v.FactBText)
			fmt.Printf("Reason: %s\n", v.Reason)
		}
	}
}
