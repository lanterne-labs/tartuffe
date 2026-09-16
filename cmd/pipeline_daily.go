package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/pipeline"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var pipelineDailySmoke bool

var pipelineDailyCmd = &cobra.Command{
	Use:   "daily",
	Short: "Run the daily pipeline once and exit.",
	Long: `Runs every phase in order: plugin ingest → stance fan-out → stance merge →
contradiction detection → regroup → audit → translation → plugin backfill.
Designed to be invoked by the host cron. Exits non-zero if any step fails so
The host flags the run as failed.

--smoke bounds every phase to a handful of items (one target, one item per
plugin, one person per sweep, no audit, no backfill) and records no
pipeline_runs row: a deploy can be exercised end to end in minutes.`,
	Annotations:   map[string]string{"needs_db": "true"},
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineDaily,
}

func init() {
	pipelineCmd.AddCommand(pipelineDailyCmd)
	pipelineDailyCmd.Flags().BoolVar(&pipelineDailySmoke, "smoke", false, "bounded end-to-end run (see --help)")
}

func runPipelineDaily(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	notifier := alerting.NewTelegramNotifierFromEnv()
	return pipeline.RunDaily(ctx, storer, notifier, pipeline.RunOptions{Smoke: pipelineDailySmoke})
}
