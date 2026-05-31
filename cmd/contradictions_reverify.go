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
	Deleted         bool   `json:"deleted,omitempty"` // true when --apply removed this row (2-of-2 drop)
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

	if reverifyApplyFlag {
		fmt.Fprintf(os.Stderr, "⚠️  --apply: dropped contradictions will be DELETED from the database (2-of-2 drop required per row).\n\n")
	}

	var keep, drop, deleted, unstable, errors int
	startedAt := time.Now()
	log.Info().Int("total", len(rows)).Bool("random", reverifyRandomFlag).Bool("apply", reverifyApplyFlag).Str("scope", scopeDescription(reverifyPersonFlag)).Msg("Starting contradiction reverify")

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

			// --apply: confirm with a second independent pass before deleting,
			// so the local model's run-to-run variance can't delete a row on a
			// fluke. Only a 2-of-2 drop is removed; a disagreeing pass keeps it.
			if reverifyApplyFlag {
				confirmKeep, confirmReason, cErr := detector.VerifyContradiction(ctx, row.FactAText, row.FactBText, row.Reason)
				switch {
				case cErr != nil:
					log.Warn().Err(cErr).Int64("contradiction_id", row.ID).Msg("Confirmation pass errored; keeping row (not deleted)")
				case confirmKeep:
					// Second pass disagrees (it would KEEP) — unstable, do not delete.
					unstable++
					log.Info().Int64("contradiction_id", row.ID).Str("confirm_reason", confirmReason).Msg("Confirmation pass disagreed (keep); row is unstable, NOT deleting")
				default:
					if err := storer.DeleteContradiction(ctx, row.ID); err != nil {
						log.Error().Err(err).Int64("contradiction_id", row.ID).Msg("Failed to delete contradiction")
					} else {
						deleted++
						line.Deleted = true
						log.Warn().Int64("contradiction_id", row.ID).Str("person", row.PersonName).Msg("Deleted contradiction (2-of-2 drop)")
					}
				}
			}
		}

		if format == "jsonl" {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
		} else {
			printReverifyRow(os.Stdout, line)
		}

		if (i+1)%10 == 0 {
			log.Info().Int("done", i+1).Int("total", len(rows)).Int("keep", keep).Int("drop", drop).Int("deleted", deleted).Int("errors", errors).Msg("Reverify progress")
		}
	}

	elapsed := time.Since(startedAt)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== REVERIFY SUMMARY ===")
	fmt.Fprintf(os.Stderr, "Checked:  %d (in %s)\n", len(rows), elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "Keep:     %d\n", keep)
	if reverifyApplyFlag {
		fmt.Fprintf(os.Stderr, "Drop:     %d  (flagged false positives)\n", drop)
		fmt.Fprintf(os.Stderr, "Deleted:  %d  (2-of-2 drop — removed from DB)\n", deleted)
		fmt.Fprintf(os.Stderr, "Unstable: %d  (passes disagreed — KEPT, not deleted)\n", unstable)
	} else {
		fmt.Fprintf(os.Stderr, "Drop:     %d  (false positives — re-run with --apply to delete)\n", drop)
	}
	fmt.Fprintf(os.Stderr, "Errors:   %d\n", errors)

	log.Info().Int("total", len(rows)).Int("keep", keep).Int("drop", drop).Int("deleted", deleted).Int("unstable", unstable).Int("errors", errors).Bool("apply", reverifyApplyFlag).Dur("elapsed", elapsed).Msg("Contradiction reverify complete")
	return nil
}

func printReverifyRow(w *os.File, line reverifyOutputLine) {
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
