package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"
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

	rows, err := storer.ListContradictions(ctx, personIDPtr, checkRandomFlag, checkMaxFlag)
	if err != nil {
		return fmt.Errorf("failed to list contradictions: %w", err)
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No contradictions to audit.")
		return nil
	}

	auditor, err := contradiction.NewOpenAIContradictionAuditorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create auditor: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)

	var counts struct {
		keep, remove, ambiguous, errors int
	}

	startedAt := time.Now()
	log.Info().
		Int("total", len(rows)).
		Bool("random", checkRandomFlag).
		Str("scope", scopeDescription(checkPersonFlag)).
		Msg("Starting contradiction audit")

	for i, row := range rows {
		result := auditor.Audit(ctx, row.FactAText, row.FactBText, row.FactADate, row.FactBDate, row.Reason)

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
			Verdict:         string(result.Verdict),
			Rule:            result.Rule,
			AuditReasoning:  result.Reasoning,
			SourceURLsA:     row.FactASourceURLs,
			SourceURLsB:     row.FactBSourceURLs,
		}
		if result.Err != nil {
			counts.errors++
			line.Error = result.Err.Error()
			log.Error().Err(result.Err).
				Int64("contradiction_id", row.ID).
				Str("person", row.PersonName).
				Msg("Audit error")
		} else {
			switch result.Verdict {
			case contradiction.VerdictKeep:
				counts.keep++
			case contradiction.VerdictRemove:
				counts.remove++
			case contradiction.VerdictAmbiguous:
				counts.ambiguous++
			}
		}

		if format == "jsonl" {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
		} else {
			printTextRow(os.Stdout, line)
		}

		if (i+1)%10 == 0 {
			log.Info().
				Int("audited", i+1).
				Int("total", len(rows)).
				Int("keep", counts.keep).
				Int("remove", counts.remove).
				Int("ambiguous", counts.ambiguous).
				Int("errors", counts.errors).
				Msg("Audit progress")
		}
	}

	printSummary(os.Stderr, len(rows), counts.keep, counts.remove, counts.ambiguous, counts.errors, time.Since(startedAt))
	return nil
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

func printSummary(w *os.File, total, keep, remove, ambiguous, errors int, elapsed time.Duration) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "=== AUDIT SUMMARY ===")
	fmt.Fprintf(w, "Audited:    %d (in %s)\n", total, elapsed.Round(time.Second))
	pct := func(n int) string {
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("  (%.1f%%)", 100*float64(n)/float64(total))
	}
	fmt.Fprintf(w, "Keep:       %d%s\n", keep, pct(keep))
	fmt.Fprintf(w, "Remove:     %d%s\n", remove, pct(remove))
	fmt.Fprintf(w, "Ambiguous:  %d%s\n", ambiguous, pct(ambiguous))
	fmt.Fprintf(w, "Errors:     %d\n", errors)
}
