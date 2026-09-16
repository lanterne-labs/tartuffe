package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/entityvalidator/sweep"
	"digital-mirror/backend/util"

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

func runEntitiesValidate(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	if entitiesValidateApply && entitiesValidateConfirm != "yes" {
		log.Fatal().Msg("--apply requires --i-know=yes to confirm destructive operation on persons table")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	report, err := sweep.Run(ctx, storer, sweep.ValidatorFromEnv(), sweep.Options{
		Concurrency:      entitiesValidateConcurrency,
		Limit:            entitiesValidateLimit,
		OnlyWithoutFacts: entitiesValidateOnlyMissing,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to validate persons")
	}

	// Optional JSON Lines report
	if entitiesValidateOutput != "" {
		if err := sweep.WriteJSONL(entitiesValidateOutput, report.Rows); err != nil {
			log.Error().Err(err).Str("path", entitiesValidateOutput).Msg("could not write report file")
		}
	}

	// Console summary
	fmt.Printf("\n=== entities validate (%.1fs over %d rows) ===\n", report.Elapsed.Seconds(), len(report.Rows))
	fmt.Printf("  validated:   %d\n", report.Validated)
	fmt.Printf("  approved:    %d\n", report.Approved)
	fmt.Printf("  rejected:    %d\n", report.Rejected)
	fmt.Printf("  skipped:     %d  (has-facts guard)\n", report.Skipped)

	rejectedRows := report.RejectedRows()
	if len(rejectedRows) > 0 {
		fmt.Println("\nRejected (first 30):")
		max := 30
		if len(rejectedRows) < max {
			max = len(rejectedRows)
		}
		for _, r := range rejectedRows[:max] {
			fmt.Printf("  [%5d] %-50s (%s)  -- %s: %s\n",
				r.ID, util.TruncateString(r.Name, 50), r.EntityType, r.Filter, util.TruncateString(r.Reason, 60))
		}
		if len(rejectedRows) > 30 {
			fmt.Printf("  ... +%d more (see --output for full list)\n", len(rejectedRows)-30)
		}
	}

	if !entitiesValidateApply {
		fmt.Println("\nDry run; pass --apply --i-know=yes to delete the rejected rows.")
		return
	}

	// Destructive path: delete rejected rows one by one (one failure does NOT
	// stop the rest).
	fmt.Printf("\nDeleting %d rejected rows from persons…\n", len(rejectedRows))
	del := sweep.DeleteRejected(ctx, storer, rejectedRows)
	fmt.Printf("  deleted:        %d\n", del.Deleted)
	fmt.Printf("  delete failed:  %d\n", del.Failed)
}
