package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/translate"
	"fmt"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	translateRunLangs  string
	translateRunEntity string
	translateRunLimit  int
)

var translateRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Fill missing or stale translations for facts, stances and contradictions.",
	Long: `The async translation phase. Scans facts, stances (label + summary) and
contradictions (reason), and for each target language that is missing or whose
source text changed (source_hash mismatch), produces a translation and stores it
in the generic translations table. Idempotent and safe to re-run.

This is the last step of the maintenance pass of the worker and never blocks generation; it can
also be run standalone.`,
	RunE: runTranslateRunCmd,
}

func init() {
	translateCmd.AddCommand(translateRunCmd)
	translateRunCmd.Flags().StringVar(&translateRunLangs, "langs", "", "Comma-separated target languages (default: en,fr)")
	translateRunCmd.Flags().StringVar(&translateRunEntity, "entity", "", "Comma-separated entity types to scope to: fact,stance,contradiction (default: all)")
	translateRunCmd.Flags().IntVar(&translateRunLimit, "limit", 0, "Max translations to fill this run (0 = no limit)")
}

func runTranslateRunCmd(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	tr, err := translate.NewOpenAITranslatorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create translator: %w", err)
	}

	targets := translate.DefaultTargetLangs
	if strings.TrimSpace(translateRunLangs) != "" {
		targets = splitLangs(translateRunLangs)
	}

	stats, err := translate.Fill(ctx, storer, tr, translate.FillOptions{
		Limit:       translateRunLimit,
		Langs:       targets,
		EntityTypes: splitLangs(translateRunEntity),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Translation phase: %d translations filled across %d translatable items (targets: %s)\n",
		stats.Filled, stats.Scanned, strings.Join(targets, ","))
	return nil
}

func splitLangs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
