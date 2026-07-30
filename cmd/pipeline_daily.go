package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var pipelineDailyCmd = &cobra.Command{
	Use:   "daily",
	Short: "Run the full pipeline once, inline, and exit.",
	Long: `Runs every pipeline step in order (news → twitter → frpolitics → stance merge →
contradictions → photos → audit → translate) over the search queue, without going
through the job queue. Exits non-zero if any step fails.

This is the manual/local escape hatch. In production the schedule enqueues a job
(` + "`dm pipeline enqueue daily`" + `) and the worker (` + "`dm pipeline worker`" + `) runs it.`,
	Annotations:   map[string]string{"needs_db": "true"},
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE:          runPipelineDaily,
}

func init() {
	pipelineCmd.AddCommand(pipelineDailyCmd)
}

func runPipelineDaily(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	notifier := alerting.NewTelegramNotifierFromEnv()

	// The zero config is the nightly pipeline: all steps, search-queue targets,
	// no date window.
	_, err = runPipeline(ctx, storer, notifier, "daily", PipelineConfig{})
	return err
}
