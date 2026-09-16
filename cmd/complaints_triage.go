package cmd

import (
	"context"
	"digital-mirror/backend/complaints"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var complaintsTriageDryRun bool

var complaintsTriageCmd = &cobra.Command{
	Use:   "triage",
	Short: "Triage user complaints with an LLM (deletes facts/contradictions/stances by default).",
	Long: `Fetches every Tally submission, asks the LLM whether each complaint is valid,
and applies the verdict:
  - delete_target:      removes the fact / contradiction / stance from the DB and deletes the Tally complaint.
  - dismiss_complaint:  keeps the DB row, deletes the Tally complaint.
  - ambiguous:          keeps both for human follow-up.

Without flags, changes are applied directly. Use --dry-run to preview.`,
	RunE: runComplaintsTriage,
}

func init() {
	complaintsCmd.AddCommand(complaintsTriageCmd)
	complaintsTriageCmd.Flags().BoolVar(&complaintsTriageDryRun, "dry-run", false, "Print verdicts only; do not delete anything")
}

func runComplaintsTriage(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	tally, err := complaints.NewTallyClientFromEnv()
	if err != nil {
		return fmt.Errorf("init Tally client: %w", err)
	}

	judge, err := complaints.NewOpenAIJudgeFromEnv()
	if err != nil {
		return fmt.Errorf("init LLM judge: %w", err)
	}

	store, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	defer store.Close()

	fmt.Println("Fetching complaints from Tally…")
	items, err := tally.ListComplaints(ctx)
	if err != nil {
		return fmt.Errorf("list Tally complaints: %w", err)
	}
	if len(items) == 0 {
		fmt.Println("No complaints to triage.")
		return nil
	}
	fmt.Printf("Found %d complaint(s). Triaging%s…\n\n", len(items), dryRunSuffix(complaintsTriageDryRun))

	report := complaints.Triage(ctx, store, tally, judge, items, complaintsTriageDryRun)

	for i, o := range report.Outcomes {
		printOutcome(i+1, o, complaintsTriageDryRun)
	}

	c := report.Counts()
	fmt.Println()
	fmt.Println("─── Summary ─────────────────────────────")
	fmt.Printf("  Total complaints processed:    %d\n", c.Total)
	fmt.Printf("  Facts deleted:                 %d\n", c.FactsDeleted)
	fmt.Printf("  Contradictions deleted:        %d\n", c.ContradictionsDeleted)
	fmt.Printf("  Stances deleted:               %d\n", c.StancesDeleted)
	fmt.Printf("  Complaints dismissed (wrong):  %d\n", c.ComplaintsDismissed)
	fmt.Printf("  Ambiguous (kept for review):   %d\n", c.Ambiguous)
	fmt.Printf("  Errors:                        %d\n", c.Errors)
	if complaintsTriageDryRun {
		fmt.Println("\n(dry-run: no changes were applied)")
	}
	return nil
}

func dryRunSuffix(dry bool) string {
	if dry {
		return " (dry-run)"
	}
	return ""
}

func printOutcome(idx int, o complaints.Outcome, dryRun bool) {
	c := o.Complaint
	target := targetDescriptor(c)
	fmt.Printf("[%d] %s — %s\n", idx, target, c.Label)
	fmt.Printf("    user: %s\n", util.TruncateString(c.Description, 200))
	if o.Err != nil {
		fmt.Printf("    ERROR: %v\n", o.Err)
		return
	}
	fmt.Printf("    verdict: %s — %s\n", o.Verdict, o.Reason)
	if dryRun {
		return
	}
	switch {
	case o.TargetDeleted && o.ComplaintDeleted:
		fmt.Printf("    applied: deleted %s and removed Tally complaint\n", c.Kind)
	case o.ComplaintDeleted:
		fmt.Printf("    applied: removed Tally complaint (target kept)\n")
	case o.Verdict == complaints.VerdictAmbiguous:
		fmt.Printf("    applied: kept both target and complaint for human review\n")
	}
}

func targetDescriptor(c complaints.Complaint) string {
	switch c.Kind {
	case complaints.KindFact:
		return fmt.Sprintf("fact #%d", c.FactID)
	case complaints.KindContradiction:
		return fmt.Sprintf("contradiction #%d", c.ContradictionID)
	case complaints.KindStance:
		return fmt.Sprintf("stance #%d", c.StanceID)
	default:
		return fmt.Sprintf("unknown kind %q", c.Kind)
	}
}
