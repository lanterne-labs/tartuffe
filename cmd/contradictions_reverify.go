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
	reverifyPersonFlag   string
	reverifyPersonIDFlag int64
	reverifyMaxFlag      int
	reverifyRandomFlag   bool
	reverifyFormatFlag   string
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

READ-ONLY: this command never deletes anything. It prints every contradiction
the verifier would now DROP (verdict=NO) so a human can review them before any
deletion is performed separately and with explicit approval.

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
}

// reverifyOutputLine is the per-row JSONL schema written to stdout.
type reverifyOutputLine struct {
	ContradictionID int64  `json:"id"`
	PersonID        int64  `json:"person_id"`
	Person          string `json:"person"`
	FactAID         int64  `json:"fact_a_id"`
	FactBID         int64  `json:"fact_b_id"`
	FactA           string `json:"fact_a"`
	FactB           string `json:"fact_b"`
	OriginalReason  string `json:"original_reason"`
	Verdict         string `json:"verdict"` // "keep" | "drop" | "error"
	Refutation      string `json:"refutation,omitempty"`
	Error           string `json:"error,omitempty"`
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
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
		normalizedName := util.NormalizeString(reverifyPersonFlag)
		personID, err := storer.GetPersonIDByName(ctx, normalizedName)
		if err != nil {
			return fmt.Errorf("failed to find person %q: %w", reverifyPersonFlag, err)
		}
		personIDPtr = &personID
	}

	rows, err := storer.ListContradictions(ctx, personIDPtr, reverifyRandomFlag, reverifyMaxFlag)
	if err != nil {
		return fmt.Errorf("failed to list contradictions: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No contradictions to reverify.")
		return nil
	}

	detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	var enc *json.Encoder
	if format == "jsonl" {
		enc = json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
	}

	var keep, drop, errors int
	startedAt := time.Now()
	log.Info().Int("total", len(rows)).Bool("random", reverifyRandomFlag).Str("scope", scopeDescription(reverifyPersonFlag)).Msg("Starting contradiction reverify")

	for i, row := range rows {
		line := reverifyOutputLine{
			ContradictionID: row.ID,
			PersonID:        row.PersonID,
			Person:          row.PersonName,
			FactAID:         row.FactAID,
			FactBID:         row.FactBID,
			FactA:           row.FactAText,
			FactB:           row.FactBText,
			OriginalReason:  row.Reason,
		}

		survives, refutation, vErr := detector.VerifyContradiction(ctx, row.FactAText, row.FactBText, row.Reason)
		switch {
		case vErr != nil:
			errors++
			line.Verdict = "error"
			line.Error = vErr.Error()
			log.Error().Err(vErr).Int64("contradiction_id", row.ID).Str("person", row.PersonName).Msg("Reverify error")
		case survives:
			keep++
			line.Verdict = "keep"
			line.Refutation = refutation
		default:
			drop++
			line.Verdict = "drop"
			line.Refutation = refutation
			log.Warn().
				Int64("contradiction_id", row.ID).
				Str("person", row.PersonName).
				Str("original_reason", row.Reason).
				Str("refutation", refutation).
				Msg("Verifier would DROP this contradiction (false positive)")
		}

		if format == "jsonl" {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
		} else {
			printReverifyRow(os.Stdout, line)
		}

		if (i+1)%10 == 0 {
			log.Info().Int("done", i+1).Int("total", len(rows)).Int("keep", keep).Int("drop", drop).Int("errors", errors).Msg("Reverify progress")
		}
	}

	elapsed := time.Since(startedAt)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== REVERIFY SUMMARY ===")
	fmt.Fprintf(os.Stderr, "Checked:  %d (in %s)\n", len(rows), elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "Keep:     %d\n", keep)
	fmt.Fprintf(os.Stderr, "Drop:     %d  (false positives — review before deleting)\n", drop)
	fmt.Fprintf(os.Stderr, "Errors:   %d\n", errors)

	log.Info().Int("total", len(rows)).Int("keep", keep).Int("drop", drop).Int("errors", errors).Dur("elapsed", elapsed).Msg("Contradiction reverify complete")
	return nil
}

func printReverifyRow(w *os.File, line reverifyOutputLine) {
	color := ansiGray
	switch line.Verdict {
	case "keep":
		color = ansiGreen
	case "drop":
		color = ansiRed
	case "error":
		color = ansiYellow
	}
	fmt.Fprintf(w, "%s[%s]%s contradiction #%d — %s\n", color, strings.ToUpper(line.Verdict), ansiReset, line.ContradictionID, line.Person)
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
