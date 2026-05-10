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
	Short: "Run the daily pipeline once and exit.",
	Long: `Runs all five daily steps in order: news → twitter → frpolitics →
contradictions → photos. Designed to be invoked by the host cron.
Exits non-zero if any step fails so the host flags the run as failed.`,
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

	return runDailyPipeline(ctx, storer, notifier)
}
