package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
)

// factsStatementDatesCmd gives the stored text facts their statement date:
// the date the person said or did what the fact states, when the text gives
// it (a 2019 tweet quoted in a 2025 article). The extractor gives it to new
// facts (mirror-fmf); the facts stored before carry only their source's date,
// which the judges read as the statement's. The dater reads the fact, its
// quote and, when the source's page is stored, the article around the quote.
// One LLM call per fact; a fact checked once (statement_date_checked_at) is
// not asked again, unless --recheck-before names the run that checked it
// without its article.
var factsStatementDatesCmd = &cobra.Command{
	Use:         "statement-dates",
	Short:       "Find the statement date of the stored text facts (the candidates' by default)",
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsStatementDates,
}

func init() {
	factsCmd.AddCommand(factsStatementDatesCmd)
	factsStatementDatesCmd.Flags().Bool("apply", false, "write the dates found (default: report only)")
	factsStatementDatesCmd.Flags().Bool("all", false, "every person's facts, not only the candidates'")
	factsStatementDatesCmd.Flags().Int("limit", 0, "at most this many facts (0: all; a dry run defaults to 20)")
	factsStatementDatesCmd.Flags().String("recheck-before", "", "also check again, with their article, the facts checked before this time (RFC 3339) without a date; give the same time to resume a run")
}

func runFactsStatementDates(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")
	all, _ := cmd.Flags().GetBool("all")
	limit, _ := cmd.Flags().GetInt("limit")
	if !apply && limit == 0 {
		limit = 20
	}
	var recheckBefore *time.Time
	if v, _ := cmd.Flags().GetString("recheck-before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return fmt.Errorf("--recheck-before: %w", err)
		}
		recheckBefore = &t
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	dater, err := core.NewStatementDaterFromEnv()
	if err != nil {
		return fmt.Errorf("statement dater: %w", err)
	}

	var mu sync.Mutex
	var checked, dated, same, failed int
	for {
		batch := 500
		if limit > 0 && limit-checked-failed < batch {
			batch = limit - checked - failed
		}
		if batch <= 0 {
			break
		}
		facts, err := storer.ListFactsForStatementDate(ctx, !all, batch, recheckBefore)
		if err != nil {
			return err
		}
		if len(facts) == 0 {
			break
		}
		checkedBefore := checked
		err = llm.ForEach(ctx, facts, func(ctx context.Context, f storage.StatementDateFact) error {
			date, why, err := dater.StatementDateInArticle(ctx, f.PersonName, f.Text, f.Quote, f.PublishedAt, f.ArticleTitle, f.Article)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", f.ID).Msg("statement date: the fact stays unchecked")
				mu.Lock()
				failed++
				mu.Unlock()
				return nil
			}
			repeats := core.SameStatementDay(date, f.PublishedAt)
			mu.Lock()
			checked++
			switch {
			case date == nil:
			case repeats:
				same++
			default:
				dated++
				fmt.Printf("%d\t%s\tsource %s\tstatement %s\t%s\n", f.ID, f.PersonName, backend.FormatFactDateISO(f.PublishedAt), backend.FormatFactDateISO(date), why)
			}
			mu.Unlock()
			if !apply {
				return nil
			}
			if date == nil || repeats {
				date = nil // the source's date stands
			}
			return storer.SetStatementDate(ctx, f.ID, date)
		})
		if err != nil {
			return err
		}
		if !apply {
			// a dry run marks nothing: the next batch would be the same facts
			break
		}
		// A failed fact stays unchecked, so the next batch lists it again: a
		// batch where no call succeeded (the API at its day cap and every seat
		// at its limit) would be listed again without end (2026-11-02: 2.4M
		// warnings in 30 min). Stop; a later run takes the facts left.
		if checked == checkedBefore {
			return fmt.Errorf("statement dates: no fact of a batch of %d could be checked (the LLM is unavailable); %d checked, %d dated before stopping", len(facts), checked, dated)
		}
		log.Info().Int("checked", checked).Int("dated", dated).Time("at", time.Now()).Msg("statement dates: batch done")
	}
	mode := "dry run"
	if apply {
		mode = "written"
	}
	fmt.Printf("%d facts checked: %d dated before their source, %d of the source's own date, %d with no date in the text; %d failed (%s)\n",
		checked, dated, same, checked-dated-same, failed, mode)
	return nil
}
