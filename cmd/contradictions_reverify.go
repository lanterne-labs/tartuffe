package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
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
	reverifyPersonFlag   string
	reverifyPersonIDFlag int64
	reverifyMaxFlag      int
	reverifyRandomFlag   bool
	reverifyFormatFlag   string
	reverifyApplyFlag    bool
)

var contradictionsReverifyCmd = &cobra.Command{
	Use:   "reverify",
	Short: "Run the adversarial verification (refute) pass over recorded contradictions and print the false positives.",
	Long: `Walks already-recorded contradictions and runs each through the same
adversarial refutation pass (VerifyContradiction) that now gates the live
detection path. Unlike 'contradictions check' (which re-judges with the full
detection rules), reverify uses the stricter refute prompt that encodes the
known false-positive buckets (numeric/measurement mismatches, procedural-scope
differences, conditional-met logic inversions, same-direction claims).

By default READ-ONLY: prints every contradiction the verifier would now DROP
(verdict=NO) without changing anything. Pass --apply to actually DELETE the
dropped rows. To guard against the local model's run-to-run variance, --apply
re-verifies each candidate a SECOND time and only deletes when BOTH passes
agree it is a false positive (2-of-2 drop); an unstable row is kept. Deleting a
contradiction also clears its fact-pair check, so the daily detector will
re-examine the pair on a later run (self-correcting if a delete was wrong).

By default prints one JSONL object per line to stdout (for jq); the summary
goes to stderr.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionReverify,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsReverifyCmd)
	contradictionsReverifyCmd.Flags().StringVarP(&reverifyPersonFlag, "person", "p", "", "Restrict to a single person by name (optional)")
	contradictionsReverifyCmd.Flags().Int64Var(&reverifyPersonIDFlag, "person-id", 0, "Restrict to a single person by numeric ID (overrides --person)")
	contradictionsReverifyCmd.Flags().IntVar(&reverifyMaxFlag, "max", 0, "Maximum number of contradictions to reverify (0 = no cap)")
	contradictionsReverifyCmd.Flags().BoolVar(&reverifyRandomFlag, "random", false, "Sample in random order instead of newest-first")
	contradictionsReverifyCmd.Flags().StringVar(&reverifyFormatFlag, "format", "jsonl", "Output format: jsonl or text")
	contradictionsReverifyCmd.Flags().BoolVar(&reverifyApplyFlag, "apply", false, "DELETE dropped contradictions (requires a confirming 2nd verification pass per row). Default is read-only.")
}

func runContradictionReverify(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	format := strings.ToLower(strings.TrimSpace(reverifyFormatFlag))
	if format != "jsonl" && format != "text" {
		return fmt.Errorf("invalid --format %q: expected 'jsonl' or 'text'", reverifyFormatFlag)
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var personIDPtr *int64
	switch {
	case reverifyPersonIDFlag > 0:
		id := reverifyPersonIDFlag
		personIDPtr = &id
	case reverifyPersonFlag != "":
		person, err := core.ResolvePersonID(ctx, storer, reverifyPersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		personIDPtr = &person.ID
	}

	if reverifyApplyFlag {
		fmt.Fprintf(os.Stderr, "⚠️  --apply: dropped contradictions will be DELETED from the database (2-of-2 drop required per row).\n\n")
	}

	result, err := contradiction.RunReverify(ctx, storer, contradiction.ReverifyOptions{
		PersonID: personIDPtr,
		Max:      reverifyMaxFlag,
		Random:   reverifyRandomFlag,
		Scope:    scopeDescription(reverifyPersonFlag),
		Apply:    reverifyApplyFlag,
		OnRow:    reverifyRowPrinter(os.Stdout, format),
	})
	if err != nil {
		return err
	}
	if result.Total == 0 {
		fmt.Fprintln(os.Stderr, "No contradictions to reverify.")
		return nil
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== REVERIFY SUMMARY ===")
	fmt.Fprintf(os.Stderr, "Checked:  %d (in %s)\n", result.Total, result.Elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "Keep:     %d\n", result.Keep)
	if reverifyApplyFlag {
		fmt.Fprintf(os.Stderr, "Drop:     %d  (flagged false positives)\n", result.Drop)
		fmt.Fprintf(os.Stderr, "Deleted:  %d  (2-of-2 drop — removed from DB)\n", result.Deleted)
		fmt.Fprintf(os.Stderr, "Unstable: %d  (passes disagreed — KEPT, not deleted)\n", result.Unstable)
	} else {
		fmt.Fprintf(os.Stderr, "Drop:     %d  (false positives — re-run with --apply to delete)\n", result.Drop)
	}
	fmt.Fprintf(os.Stderr, "Errors:   %d\n", result.Errors)
	return nil
}

// reverifyRowPrinter writes each reverified row to w as one JSONL object or
// as a colored text block.
func reverifyRowPrinter(w io.Writer, format string) func(contradiction.ReverifyRow) error {
	if format == "jsonl" {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return func(line contradiction.ReverifyRow) error {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
			return nil
		}
	}
	return func(line contradiction.ReverifyRow) error {
		printReverifyRow(w, line)
		return nil
	}
}

func printReverifyRow(w io.Writer, line contradiction.ReverifyRow) {
	color := ansiGray
	label := strings.ToUpper(line.Verdict)
	switch line.Verdict {
	case "keep":
		color = ansiGreen
	case "drop":
		color = ansiRed
		if line.Deleted {
			label = "DELETED"
		}
	case "error":
		color = ansiYellow
	}
	fmt.Fprintf(w, "%s[%s]%s contradiction #%d — %s\n", color, label, ansiReset, line.ContradictionID, line.Person)
	fmt.Fprintf(w, "  Fact A (#%d): %s\n", line.FactAID, line.FactA)
	fmt.Fprintf(w, "  Fact B (#%d): %s\n", line.FactBID, line.FactB)
	fmt.Fprintf(w, "  Original: %s\n", line.OriginalReason)
	if line.Refutation != "" {
		fmt.Fprintf(w, "  Verifier: %s\n", line.Refutation)
	}
	if line.Error != "" {
		fmt.Fprintf(w, "  ERROR:    %s\n", line.Error)
	}
	fmt.Fprintln(w)
}
