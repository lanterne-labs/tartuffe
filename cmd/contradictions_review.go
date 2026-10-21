package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	reviewReviewerFlag string
	reviewSinceFlag    string
	reviewPersonFlag   string
	reviewMaxFlag      int
)

var contradictionsReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review stored contradictions by hand to measure the detection precision.",
	Long: `Records verdicts on stored contradictions: real (the person contradicted
themselves), debatable (a reader could argue either way), false (no
contradiction). The verdicts keep the facts as reviewed, so the precision of
the detection stays measured after a reset or an audit (Grafana, Quality).

  dm contradictions review sample --reviewer alice --max 30 > todo.jsonl
  (add "verdict" and an optional "note" to each line)
  dm contradictions review record --reviewer alice < todo.jsonl`,
}

var contradictionsReviewSampleCmd = &cobra.Command{
	Use:   "sample",
	Short: "Print, as JSONL, a random sample of the contradictions the reviewer has not reviewed.",
	Long: `Prints one JSON object per line: id, person, the two facts with their dates
and sources, and the detection reason. The sample is random among the
contradictions detected since --since that --reviewer has not reviewed.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionsReviewSample,
}

var contradictionsReviewRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Record verdicts read as JSONL on stdin.",
	Long: `Reads one JSON object per line on stdin, with at least "id" (the
contradiction) and "verdict" (real, debatable or false), and an optional
"note"; the other fields (as printed by sample) are ignored. A contradiction
the reviewer already reviewed gets the new verdict.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionsReviewRecord,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsReviewCmd)
	contradictionsReviewCmd.AddCommand(contradictionsReviewSampleCmd, contradictionsReviewRecordCmd)
	contradictionsReviewCmd.PersistentFlags().StringVar(&reviewReviewerFlag, "reviewer", "", "Who gives the verdicts (a person's name, or a model such as claude-opus-5-5) (required)")
	contradictionsReviewCmd.MarkPersistentFlagRequired("reviewer")
	contradictionsReviewSampleCmd.Flags().StringVar(&reviewSinceFlag, "since", "", "Only contradictions detected on or after this date (YYYY-MM-DD)")
	contradictionsReviewSampleCmd.Flags().StringVarP(&reviewPersonFlag, "person", "p", "", "Only the contradictions of this person")
	contradictionsReviewSampleCmd.Flags().IntVar(&reviewMaxFlag, "max", 30, "Sample size (0 = every unreviewed contradiction)")
}

// reviewSampleRow is one line printed by review sample.
type reviewSampleRow struct {
	ID       int64    `json:"id"`
	Person   string   `json:"person"`
	Detected string   `json:"detected"`
	FactA    string   `json:"fact_a"`
	DateA    string   `json:"date_a,omitempty"`
	SourcesA []string `json:"sources_a,omitempty"`
	FactB    string   `json:"fact_b"`
	DateB    string   `json:"date_b,omitempty"`
	SourcesB []string `json:"sources_b,omitempty"`
	Reason   string   `json:"reason"`
}

func runContradictionsReviewSample(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	var since time.Time
	if reviewSinceFlag != "" {
		t, err := time.Parse("2006-01-02", reviewSinceFlag)
		if err != nil {
			return fmt.Errorf("invalid --since %q: %w", reviewSinceFlag, err)
		}
		since = t
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var personID *int64
	if reviewPersonFlag != "" {
		person, err := core.ResolvePersonID(ctx, storer, reviewPersonFlag, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		personID = &person.ID
	}
	rows, err := storer.ListContradictionsToReview(ctx, reviewReviewerFlag, since, personID, reviewMaxFlag)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(reviewSampleRow{
			ID: r.ID, Person: r.PersonName, Detected: r.DetectedAt.Format("2006-01-02"),
			FactA: r.FactAText, DateA: formatOptionalDate(r.FactADate), SourcesA: r.FactASourceURLs,
			FactB: r.FactBText, DateB: formatOptionalDate(r.FactBDate), SourcesB: r.FactBSourceURLs,
			Reason: r.Reason,
		}); err != nil {
			return fmt.Errorf("writing the sample: %w", err)
		}
	}
	fmt.Fprintf(os.Stderr, "%d contradictions to review\n", len(rows))
	return nil
}

func runContradictionsReviewRecord(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	counts := map[string]int{}
	var failed int
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var r storage.ContradictionReview
		if err := json.Unmarshal([]byte(text), &r); err != nil {
			fmt.Fprintf(os.Stderr, "line %d: %v\n", line, err)
			failed++
			continue
		}
		if err := storer.RecordContradictionReview(ctx, reviewReviewerFlag, r); err != nil {
			fmt.Fprintf(os.Stderr, "line %d: %v\n", line, err)
			failed++
			continue
		}
		counts[r.Verdict]++
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	fmt.Fprintf(os.Stderr, "recorded: %d real, %d debatable, %d false; %d failed\n",
		counts["real"], counts["debatable"], counts["false"], failed)
	if failed > 0 {
		return fmt.Errorf("%d lines failed", failed)
	}
	return nil
}

func formatOptionalDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
