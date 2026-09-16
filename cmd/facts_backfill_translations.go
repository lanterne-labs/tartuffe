package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/translate"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var backfillTranslationsLimit int

var factsBackfillTranslationsCmd = &cobra.Command{
	Use:   "backfill-translations",
	Short: "Fill missing English translations (fact_text_en) for non-English facts.",
	Long: `Finds non-English facts whose fact_text_en is NULL — the gaps the daily
extraction left behind when the model dropped the translation field — and fills
each via a cheap translate-only LLM call. Idempotent and safe to re-run; the
daily pipeline also runs this as a final repair step so the gap self-heals.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsBackfillTranslations,
}

func init() {
	factsCmd.AddCommand(factsBackfillTranslationsCmd)
	factsBackfillTranslationsCmd.Flags().IntVar(&backfillTranslationsLimit, "limit", 5000, "Maximum facts to translate in this run")
}

func runFactsBackfillTranslations(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	translated, failed, err := translate.RepairMissingEnglish(ctx, storer, backfillTranslationsLimit)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Translation backfill: %d filled, %d failed.\n", translated, failed)
	return nil
}
