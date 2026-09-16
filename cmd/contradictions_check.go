package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	checkPersonFlag   string
	checkPersonIDFlag int64
	checkMaxFlag      int
	checkRandomFlag   bool
	checkFormatFlag   string
	checkApplyFlag    bool
)

var contradictionsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Audit recorded contradictions with the LLM and flag false positives.",
	Long: `Walks already-recorded contradictions and asks the LLM to re-judge each
one against the same rule set used at detection time. Outputs a per-row verdict
(keep / remove / ambiguous) plus reasoning. Useful for assessing detection
quality without modifying the database.

Output goes to stdout (one JSONL object per line by default, for jq); the final
summary is printed to stderr so stdout stays clean for piping.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionCheck,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsCheckCmd)
	contradictionsCheckCmd.Flags().StringVarP(&checkPersonFlag, "person", "p", "", "Restrict audit to a single person by name (optional)")
	contradictionsCheckCmd.Flags().Int64Var(&checkPersonIDFlag, "person-id", 0, "Restrict audit to a single person by numeric ID (overrides --person; useful for environments where shell quoting drops spaces in names)")
	contradictionsCheckCmd.Flags().IntVar(&checkMaxFlag, "max", 0, "Maximum number of contradictions to audit (0 = no cap)")
	contradictionsCheckCmd.Flags().BoolVar(&checkRandomFlag, "random", false, "Sample contradictions in random order (instead of newest-first)")
	contradictionsCheckCmd.Flags().StringVar(&checkFormatFlag, "format", "jsonl", "Output format: jsonl or text")
	contradictionsCheckCmd.Flags().BoolVar(&checkApplyFlag, "apply", false, "DELETE auditor-confirmed false positives (verdict=remove, then a confirming 2nd auditor pass; only 2-of-2 removes are deleted). Records a negative pair check so the detector won't re-raise them, and recomputes affected honesty scores. Default is read-only.")
}

func runContradictionCheck(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	format := strings.ToLower(strings.TrimSpace(checkFormatFlag))
	if format != "jsonl" && format != "text" {
		return fmt.Errorf("invalid --format %q: expected 'jsonl' or 'text'", checkFormatFlag)
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var personIDPtr *int64
	switch {
	case checkPersonIDFlag > 0:
		id := checkPersonIDFlag
		personIDPtr = &id
	case checkPersonFlag != "":
		person, err := core.ResolvePersonID(ctx, storer, checkPersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		personIDPtr = &person.ID
	}

	if checkApplyFlag {
		fmt.Fprintf(os.Stderr, "⚠️  --apply: auditor-confirmed false positives will be DELETED from the database (2-of-2 remove required per row).\n\n")
	}

	_, err = executeContradictionAudit(ctx, storer, ContradictionAuditOpts{
		PersonID:     personIDPtr,
		Max:          checkMaxFlag,
		Random:       checkRandomFlag,
		Output:       os.Stdout,
		Format:       format,
		Scope:        scopeDescription(checkPersonFlag),
		RecordMetric: false, // CLI runs are ad-hoc; only the periodic pipeline pass records job_metrics
		Apply:        checkApplyFlag,
	})
	return err
}

// ContradictionAuditOpts configures executeContradictionAudit: the
// contradiction.AuditOptions plus where and how per-row output is written.
type ContradictionAuditOpts struct {
	// PersonID restricts the audit to one person when set.
	PersonID *int64
	// Max caps the number of contradictions audited (0 = no cap).
	Max int
	// Random samples contradictions in random order instead of newest-first.
	Random bool
	// Output is where per-row results are written. Nil = silent (pipeline mode).
	Output io.Writer
	// Format is "jsonl" or "text"; only consulted when Output != nil.
	Format string
	// Scope is a human-readable label for the log line ("all persons", "Marine Le Pen", etc.).
	Scope string
	// RecordMetric — see contradiction.AuditOptions.RecordMetric.
	RecordMetric bool
	// Apply — see contradiction.AuditOptions.Apply.
	Apply bool
}

// ContradictionAuditResult is the outcome of an audit pass.
type ContradictionAuditResult = contradiction.AuditRunResult

// executeContradictionAudit runs contradiction.RunAudit, writing each verdict
// to opts.Output in opts.Format and the summary to stderr when Output is set.
func executeContradictionAudit(ctx context.Context, storer *storage.PostgresStorer, opts ContradictionAuditOpts) (*ContradictionAuditResult, error) {
	var onRow func(contradiction.AuditRow) error
	if opts.Output != nil {
		onRow = auditRowPrinter(opts.Output, opts.Format)
	}

	result, err := contradiction.RunAudit(ctx, storer, contradiction.AuditOptions{
		PersonID:     opts.PersonID,
		Max:          opts.Max,
		Random:       opts.Random,
		Scope:        opts.Scope,
		RecordMetric: opts.RecordMetric,
		Apply:        opts.Apply,
		OnRow:        onRow,
	})
	if err != nil {
		return result, err
	}

	if opts.Output != nil {
		if result.Total == 0 {
			fmt.Fprintln(os.Stderr, "No contradictions to audit.")
		} else {
			printSummary(os.Stderr, result, opts.Apply)
		}
	}
	return result, nil
}

// auditRowPrinter writes each audited row to w as one JSONL object or as a
// colored text block.
func auditRowPrinter(w io.Writer, format string) func(contradiction.AuditRow) error {
	if format == "jsonl" {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return func(line contradiction.AuditRow) error {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
			return nil
		}
	}
	return func(line contradiction.AuditRow) error {
		printTextRow(w, line)
		return nil
	}
}

func scopeDescription(person string) string {
	if person == "" {
		return "all persons"
	}
	return person
}

func printTextRow(w io.Writer, line contradiction.AuditRow) {
	color := ansiGray
	switch line.Verdict {
	case string(contradiction.VerdictKeep):
		color = ansiGreen
	case string(contradiction.VerdictRemove):
		color = ansiRed
	case string(contradiction.VerdictAmbiguous):
		color = ansiYellow
	}
	verdict := strings.ToUpper(line.Verdict)
	if verdict == "" {
		verdict = "ERROR"
	}
	if line.Deleted {
		verdict = "DELETED"
	}
	fmt.Fprintf(w, "%s[%s]%s contradiction #%d — %s\n", color, verdict, ansiReset, line.ContradictionID, line.Person)
	fmt.Fprintf(w, "  Fact A [%s] (#%d): %s\n", orUnknown(line.DateA), line.FactAID, line.FactA)
	fmt.Fprintf(w, "  Fact B [%s] (#%d): %s\n", orUnknown(line.DateB), line.FactBID, line.FactB)
	fmt.Fprintf(w, "  Original: %s\n", line.OriginalReason)
	if line.Rule != "" {
		fmt.Fprintf(w, "  Rule:     %s\n", line.Rule)
	}
	fmt.Fprintf(w, "  Audit:    %s\n", line.AuditReasoning)
	if line.Error != "" {
		fmt.Fprintf(w, "  ERROR:    %s\n", line.Error)
	}
	fmt.Fprintln(w)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func printSummary(w *os.File, r *ContradictionAuditResult, apply bool) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "=== AUDIT SUMMARY ===")
	fmt.Fprintf(w, "Audited:    %d (in %s)\n", r.Total, r.Elapsed.Round(time.Second))
	pct := func(n int) string {
		if r.Total == 0 {
			return ""
		}
		return fmt.Sprintf("  (%.1f%%)", 100*float64(n)/float64(r.Total))
	}
	fmt.Fprintf(w, "Keep:       %d%s\n", r.Keep, pct(r.Keep))
	fmt.Fprintf(w, "Remove:     %d%s\n", r.Remove, pct(r.Remove))
	fmt.Fprintf(w, "Ambiguous:  %d%s\n", r.Ambiguous, pct(r.Ambiguous))
	fmt.Fprintf(w, "Errors:     %d\n", r.Errors)
	if apply {
		fmt.Fprintf(w, "Deleted:    %d  (2-of-2 remove — removed from DB + score recomputed)\n", r.Deleted)
		fmt.Fprintf(w, "Unstable:   %d  (confirm pass disagreed — KEPT)\n", r.Unstable)
	}
}
