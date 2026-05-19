package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"digital-mirror/backend/entityvalidator"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	entitiesValidateApply       bool
	entitiesValidateConcurrency int
	entitiesValidateLimit       int
	entitiesValidateOutput      string
	entitiesValidateConfirm     string
	entitiesValidateOnlyMissing bool
)

var entitiesValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Run the wiki+LLM validator on every existing person; report (default) or delete (--apply).",
	Long: `Walks the persons table and runs each row through the same entityvalidator
pipeline the extractor uses pre-insert (WikiFilter → LLMFilter). The default
mode is dry-run: prints a JSON report of every decision plus a summary, no
mutations. Passing --apply will DELETE every rejected row from prod (and you'll
need to also pass --i-know to confirm — guard against accidental destructive
runs over SSH).

  dm entities validate                       # dry-run, full table
  dm entities validate --limit 50            # dry-run, first 50 rows
  dm entities validate --apply --i-know yes  # delete rejected rows from prod

The validator uses the same code path as the daily pipeline, so what this
deletes is exactly what extraction would have rejected at insert time.`,
	Args: cobra.NoArgs,
	Run:  runEntitiesValidate,
}

func init() {
	entitiesCmd.AddCommand(entitiesValidateCmd)
	entitiesValidateCmd.Flags().BoolVar(&entitiesValidateApply, "apply", false,
		"Actually delete rejected rows from the persons table. Default is dry-run.")
	entitiesValidateCmd.Flags().IntVar(&entitiesValidateConcurrency, "concurrency", 4,
		"Number of persons to validate in parallel (wiki + LLM calls each).")
	entitiesValidateCmd.Flags().IntVar(&entitiesValidateLimit, "limit", 0,
		"Validate only the first N rows (alphabetical). 0 = no limit.")
	entitiesValidateCmd.Flags().StringVar(&entitiesValidateOutput, "output", "",
		"Optional path to write the per-row JSON report. Defaults to stdout summary only.")
	entitiesValidateCmd.Flags().StringVar(&entitiesValidateConfirm, "i-know", "",
		"Required value 'yes' to confirm destructive --apply runs.")
	entitiesValidateCmd.Flags().BoolVar(&entitiesValidateOnlyMissing, "only-without-facts", true,
		"Skip persons that already have facts attached (they're presumed validated by prior use).")
}

type validateReportRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	Real       bool   `json:"real"`
	Filter     string `json:"filter"`
	Reason     string `json:"reason"`
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

func runEntitiesValidate(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	if entitiesValidateApply && entitiesValidateConfirm != "yes" {
		log.Fatal().Msg("--apply requires --i-know=yes to confirm destructive operation on persons table")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to list persons")
	}
	if entitiesValidateLimit > 0 && entitiesValidateLimit < len(persons) {
		persons = persons[:entitiesValidateLimit]
	}
	log.Info().Int("count", len(persons)).Msg("Validating persons through wiki+LLM pipeline")

	// Build the validator separately so we apply the same code the storer uses,
	// but with DefaultDecision tuned for cleanup (Reject on unknown — i.e. when
	// all filters Defer, treat the candidate as suspect rather than safe).
	v := entityvalidator.BuildFromEnv()
	v.DefaultDecision = entityvalidator.Reject

	// fact-count skip: hit the DB once for the whole list rather than per-row.
	factCounts := map[int64]int{}
	if entitiesValidateOnlyMissing {
		factCounts = collectFactCounts(ctx, storer, persons)
	}

	concurrency := entitiesValidateConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	type job struct {
		idx    int
		person storage.PersonInfo
	}
	jobs := make(chan job)
	results := make([]validateReportRow, len(persons))
	var (
		wg              sync.WaitGroup
		nApproved       atomic.Int64
		nRejected       atomic.Int64
		nSkipped        atomic.Int64
		nFiltered       atomic.Int64
		nDeleted        atomic.Int64
		nDeleteFailed   atomic.Int64
	)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				row := validateReportRow{
					ID:         j.person.ID,
					Name:       j.person.Name,
					EntityType: j.person.EntityType,
				}
				if entitiesValidateOnlyMissing && factCounts[j.person.ID] > 0 {
					row.Skipped = true
					row.SkipReason = fmt.Sprintf("has %d existing facts", factCounts[j.person.ID])
					results[j.idx] = row
					nSkipped.Add(1)
					continue
				}
				verdict := v.Validate(ctx, j.person.Name, j.person.EntityType)
				row.Real = verdict.Real
				row.Filter = verdict.Filter
				row.Reason = verdict.Reason
				results[j.idx] = row
				if verdict.Real {
					nApproved.Add(1)
				} else {
					nRejected.Add(1)
				}
				nFiltered.Add(1)
			}
		}()
	}
	t0 := time.Now()
	for i, p := range persons {
		jobs <- job{idx: i, person: p}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(t0)

	// Optional JSON Lines report
	if entitiesValidateOutput != "" {
		writeJSONLReport(entitiesValidateOutput, results)
	}

	// Console summary
	fmt.Printf("\n=== entities validate (%.1fs over %d rows) ===\n", elapsed.Seconds(), len(persons))
	fmt.Printf("  validated:   %d\n", nFiltered.Load())
	fmt.Printf("  approved:    %d\n", nApproved.Load())
	fmt.Printf("  rejected:    %d\n", nRejected.Load())
	fmt.Printf("  skipped:     %d  (has-facts guard)\n", nSkipped.Load())

	rejectedRows := make([]validateReportRow, 0, nRejected.Load())
	for _, r := range results {
		if !r.Skipped && !r.Real {
			rejectedRows = append(rejectedRows, r)
		}
	}
	if len(rejectedRows) > 0 {
		fmt.Println("\nRejected (first 30):")
		max := 30
		if len(rejectedRows) < max {
			max = len(rejectedRows)
		}
		for _, r := range rejectedRows[:max] {
			fmt.Printf("  [%5d] %-50s (%s)  -- %s: %s\n",
				r.ID, truncString(r.Name, 50), r.EntityType, r.Filter, truncString(r.Reason, 60))
		}
		if len(rejectedRows) > 30 {
			fmt.Printf("  ... +%d more (see --output for full list)\n", len(rejectedRows)-30)
		}
	}

	if !entitiesValidateApply {
		fmt.Println("\nDry run; pass --apply --i-know=yes to delete the rejected rows.")
		return
	}

	// Destructive path: delete rejected rows one by one (per the user's
	// robustness guidance, one failure does NOT stop the rest).
	fmt.Printf("\nDeleting %d rejected rows from persons…\n", len(rejectedRows))
	for _, r := range rejectedRows {
		if err := deletePersonByID(ctx, storer, r.ID); err != nil {
			nDeleteFailed.Add(1)
			log.Error().Err(err).Int64("id", r.ID).Str("name", r.Name).Msg("delete failed")
			continue
		}
		nDeleted.Add(1)
	}
	fmt.Printf("  deleted:        %d\n", nDeleted.Load())
	fmt.Printf("  delete failed:  %d\n", nDeleteFailed.Load())
}

// collectFactCounts batches a single query for fact counts of all candidate IDs.
func collectFactCounts(ctx context.Context, s *storage.PostgresStorer, persons []storage.PersonInfo) map[int64]int {
	ids := make([]int64, len(persons))
	for i, p := range persons {
		ids[i] = p.ID
	}
	counts, err := s.GetFactCountsByPersonIDs(ctx, ids)
	if err != nil {
		log.Warn().Err(err).Msg("Could not load fact counts; treating all persons as zero-facts")
		return map[int64]int{}
	}
	return counts
}

func deletePersonByID(ctx context.Context, s *storage.PostgresStorer, id int64) error {
	return s.DeletePersonByID(ctx, id)
}

func writeJSONLReport(path string, rows []validateReportRow) {
	f, err := os.Create(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("could not open report file")
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		_ = enc.Encode(r)
	}
	log.Info().Str("path", path).Int("rows", len(rows)).Msg("Wrote validation report")
}

func truncString(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
