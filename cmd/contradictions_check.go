package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
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

// auditOutputLine is the per-row JSONL schema written to stdout.
type auditOutputLine struct {
	ContradictionID int64    `json:"id"`
	PersonID        int64    `json:"person_id"`
	Person          string   `json:"person"`
	FactAID         int64    `json:"fact_a_id"`
	FactBID         int64    `json:"fact_b_id"`
	FactA           string   `json:"fact_a"`
	FactB           string   `json:"fact_b"`
	DateA           string   `json:"date_a,omitempty"`
	DateB           string   `json:"date_b,omitempty"`
	OriginalReason  string   `json:"original_reason"`
	Verdict         string   `json:"verdict"`
	Rule            string   `json:"rule,omitempty"`
	AuditReasoning  string   `json:"audit_reasoning"`
	Deleted         bool     `json:"deleted,omitempty"` // true when --apply removed this row (2-of-2 remove)
	Error           string   `json:"error,omitempty"`
	SourceURLsA     []string `json:"source_urls_a,omitempty"`
	SourceURLsB     []string `json:"source_urls_b,omitempty"`
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
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
		normalizedName := util.NormalizeString(checkPersonFlag)
		personID, err := storer.GetPersonIDByName(ctx, normalizedName)
		if err != nil {
			return fmt.Errorf("failed to find person %q: %w", checkPersonFlag, err)
		}
		personIDPtr = &personID
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

// ContradictionAuditOpts configures executeContradictionAudit. The same code
// path serves the `dm contradictions check` CLI and the daily-pipeline auditor
// step — the differences are: who provides the storer, whether stdout output
// is wanted, and whether the run gets recorded to job_metrics (which gates the
// pipeline cadence).
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
	// RecordMetric — when true, a job_metrics row is inserted after the audit
	// completes (job_type="contradiction-audit"). The daily pipeline gate
	// reads this timestamp to decide whether enough time has passed since the
	// last run; CLI invocations leave it false.
	RecordMetric bool
	// Apply — when true, a verdict=remove triggers a confirming SECOND auditor
	// pass and, on a 2-of-2 remove, the contradiction is DELETED (via
	// DismissContradictionAsFalsePositive: records a negative pair check so the
	// detector won't re-raise it, and recomputes the owner's honesty score). A
	// disagreeing second pass keeps the row (counted as unstable). Default false
	// (read-only advisory).
	Apply bool
}

// ContradictionAuditResult is the return value of executeContradictionAudit —
// callers (especially the daily pipeline) use the counts for logging and
// Telegram summaries. RemoveIDs is the actionable bit: each ID is a
// contradiction the auditor flagged as a false positive (verdict=remove).
type ContradictionAuditResult struct {
	Total     int
	Keep      int
	Remove    int
	Ambiguous int
	Errors    int
	RemoveIDs []int64
	// Deleted counts rows removed under Apply (2-of-2 remove). Unstable counts
	// remove verdicts whose confirming pass disagreed (kept). Both stay 0 when
	// Apply is false.
	Deleted  int
	Unstable int
	Elapsed  time.Duration
}

// executeContradictionAudit walks every recorded contradiction matching opts,
// asks the LLM auditor to re-judge it against the current ContradictionRules,
// and emits per-verdict Prometheus counters. Does NOT delete anything — when
// the auditor flags a row as a false positive (VerdictRemove), the ID lands
// in result.RemoveIDs so a downstream caller can act (today: a human, eventually:
// `dm contradictions check --apply`).
func executeContradictionAudit(ctx context.Context, storer *storage.PostgresStorer, opts ContradictionAuditOpts) (*ContradictionAuditResult, error) {
	rows, err := storer.ListContradictions(ctx, opts.PersonID, opts.Random, opts.Max)
	if err != nil {
		return nil, fmt.Errorf("failed to list contradictions: %w", err)
	}

	result := &ContradictionAuditResult{Total: len(rows)}
	if len(rows) == 0 {
		if opts.Output != nil {
			fmt.Fprintln(os.Stderr, "No contradictions to audit.")
		}
		log.Info().Msg("Contradiction audit: no contradictions in scope, nothing to do")
		if opts.RecordMetric {
			recordAuditJobMetrics(ctx, storer, result)
			metrics.IncrementContradictionAuditRuns()
		}
		return result, nil
	}

	auditor, err := contradiction.NewOpenAIContradictionAuditorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create auditor: %w", err)
	}

	var enc *json.Encoder
	if opts.Output != nil && opts.Format == "jsonl" {
		enc = json.NewEncoder(opts.Output)
		enc.SetEscapeHTML(false)
	}

	startedAt := time.Now()
	log.Info().
		Int("total", len(rows)).
		Bool("random", opts.Random).
		Str("scope", opts.Scope).
		Msg("Starting contradiction audit")

	for i, row := range rows {
		auditResult := auditor.Audit(ctx, row.FactAText, row.FactBText, row.FactADate, row.FactBDate, row.Reason)

		line := auditOutputLine{
			ContradictionID: row.ID,
			PersonID:        row.PersonID,
			Person:          row.PersonName,
			FactAID:         row.FactAID,
			FactBID:         row.FactBID,
			FactA:           row.FactAText,
			FactB:           row.FactBText,
			DateA:           formatDateForOutput(row.FactADate),
			DateB:           formatDateForOutput(row.FactBDate),
			OriginalReason:  row.Reason,
			Verdict:         string(auditResult.Verdict),
			Rule:            auditResult.Rule,
			AuditReasoning:  auditResult.Reasoning,
			SourceURLsA:     row.FactASourceURLs,
			SourceURLsB:     row.FactBSourceURLs,
		}
		if auditResult.Err != nil {
			result.Errors++
			line.Error = auditResult.Err.Error()
			metrics.IncrementContradictionAudits("error")
			log.Error().Err(auditResult.Err).
				Int64("contradiction_id", row.ID).
				Str("person", row.PersonName).
				Msg("Audit error")
		} else {
			switch auditResult.Verdict {
			case contradiction.VerdictKeep:
				result.Keep++
				metrics.IncrementContradictionAudits("keep")
			case contradiction.VerdictRemove:
				result.Remove++
				result.RemoveIDs = append(result.RemoveIDs, row.ID)
				metrics.IncrementContradictionAudits("remove")
				log.Warn().
					Int64("contradiction_id", row.ID).
					Str("person", row.PersonName).
					Str("rule", auditResult.Rule).
					Str("audit_reasoning", auditResult.Reasoning).
					Msg("Auditor flagged contradiction as false positive (verdict=remove)")

				// --apply: confirm with a second independent auditor pass before
				// deleting, so the local model's run-to-run variance can't remove a
				// row on a fluke. Only a 2-of-2 remove is deleted; a disagreeing
				// pass keeps it (unstable).
				if opts.Apply {
					line.Deleted = applyAuditRemoval(ctx, storer, auditor, row, result)
				}
			case contradiction.VerdictAmbiguous:
				result.Ambiguous++
				metrics.IncrementContradictionAudits("ambiguous")
			}
		}

		if opts.Output != nil {
			if opts.Format == "jsonl" {
				if err := enc.Encode(line); err != nil {
					return result, fmt.Errorf("failed to encode JSONL line: %w", err)
				}
			} else {
				printTextRow(os.Stdout, line)
			}
		}

		if (i+1)%10 == 0 {
			log.Info().
				Int("audited", i+1).
				Int("total", len(rows)).
				Int("keep", result.Keep).
				Int("remove", result.Remove).
				Int("ambiguous", result.Ambiguous).
				Int("errors", result.Errors).
				Msg("Audit progress")
		}
	}

	result.Elapsed = time.Since(startedAt)

	if opts.Output != nil {
		printSummary(os.Stderr, result, opts.Apply)
	}

	if opts.RecordMetric {
		recordAuditJobMetrics(ctx, storer, result)
		metrics.IncrementContradictionAuditRuns()
	}

	log.Info().
		Int("total", result.Total).
		Int("keep", result.Keep).
		Int("remove", result.Remove).
		Int("ambiguous", result.Ambiguous).
		Int("errors", result.Errors).
		Dur("elapsed", result.Elapsed).
		Ints64("remove_ids", result.RemoveIDs).
		Msg("Contradiction audit pass complete")

	return result, nil
}

// applyAuditRemoval runs a confirming second auditor pass on a row the first
// pass flagged for removal and, on a 2-of-2 remove, dismisses the contradiction
// as a false positive (delete + negative pair check + score recompute). It
// mutates result.Deleted / result.Unstable and returns true iff the row was
// deleted. An errored or disagreeing confirmation keeps the row.
func applyAuditRemoval(
	ctx context.Context,
	storer *storage.PostgresStorer,
	auditor *contradiction.OpenAIContradictionAuditor,
	row storage.ContradictionAuditRow,
	result *ContradictionAuditResult,
) bool {
	confirm := auditor.Audit(ctx, row.FactAText, row.FactBText, row.FactADate, row.FactBDate, row.Reason)
	switch {
	case confirm.Err != nil:
		result.Unstable++
		log.Warn().Err(confirm.Err).Int64("contradiction_id", row.ID).
			Msg("Confirmation audit pass errored; keeping row (not deleted)")
		return false
	case confirm.Verdict != contradiction.VerdictRemove:
		result.Unstable++
		log.Info().Int64("contradiction_id", row.ID).Str("confirm_verdict", string(confirm.Verdict)).
			Str("confirm_reasoning", confirm.Reasoning).
			Msg("Confirmation audit pass disagreed; row is unstable, NOT deleting")
		return false
	default:
		if err := storer.DismissContradictionAsFalsePositive(ctx, row.ID); err != nil {
			log.Error().Err(err).Int64("contradiction_id", row.ID).
				Msg("Failed to dismiss contradiction as false positive")
			return false
		}
		result.Deleted++
		log.Warn().Int64("contradiction_id", row.ID).Str("person", row.PersonName).
			Msg("Dismissed contradiction as false positive (2-of-2 remove)")
		return true
	}
}

// recordAuditJobMetrics persists per-verdict counts so the next pipeline tick
// can read GetLatestJobRunTime("contradiction-audit") and decide whether to
// skip. Best-effort: a write failure logs but does not abort the caller —
// missing the timestamp just means the next pipeline run will re-attempt.
func recordAuditJobMetrics(ctx context.Context, storer *storage.PostgresStorer, r *ContradictionAuditResult) {
	jobMetrics := []storage.JobMetric{
		{JobType: "contradiction-audit", Target: "", Metric: "total", Value: float64(r.Total)},
		{JobType: "contradiction-audit", Target: "", Metric: "keep", Value: float64(r.Keep)},
		{JobType: "contradiction-audit", Target: "", Metric: "remove", Value: float64(r.Remove)},
		{JobType: "contradiction-audit", Target: "", Metric: "ambiguous", Value: float64(r.Ambiguous)},
		{JobType: "contradiction-audit", Target: "", Metric: "errors", Value: float64(r.Errors)},
	}
	if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
		log.Error().Err(err).Msg("Failed to record contradiction-audit job metrics")
	}
}

func scopeDescription(person string) string {
	if person == "" {
		return "all persons"
	}
	return person
}

func formatDateForOutput(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiGray   = "\x1b[90m"
)

func printTextRow(w *os.File, line auditOutputLine) {
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
