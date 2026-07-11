package cmd

import (
	"context"
	"digital-mirror/backend/storage"
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

This is the LAST step of 'dm pipeline daily' and never blocks generation; it can
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
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

	filled, scanned, err := runTranslateFill(ctx, storer, tr, targets, splitLangs(translateRunEntity), translateRunLimit)
	if err != nil {
		return err
	}
	fmt.Printf("Translation phase: %d translations filled across %d translatable items (targets: %s)\n",
		filled, scanned, strings.Join(targets, ","))
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

// runTranslateFill is the shared core used by the CLI and the daily pipeline's
// final step. For each translatable item it fills every target language that is
// missing or stale (source_hash != current hash), best-effort per language.
func runTranslateFill(ctx context.Context, storer *storage.PostgresStorer, tr translate.Translator, targets, entityTypes []string, limit int) (filled int, scanned int, err error) {
	items, err := storer.ListTranslatableItems(ctx, entityTypes...)
	if err != nil {
		return 0, 0, fmt.Errorf("list translatable items: %w", err)
	}
	for _, it := range items {
		if limit > 0 && filled >= limit {
			break
		}
		hash := translate.SourceHash(it.SourceText)
		// A stored translation counts as done only if it was produced from the
		// CURRENT source text (hash match); otherwise it is stale and refilled.
		done := make(map[string]bool, len(it.Existing))
		for lang, h := range it.Existing {
			if h == hash {
				done[lang] = true
			}
		}
		results := translate.FillMissing(ctx, tr, it.SourceText, it.SrcLang, targets, done)
		for lang, txt := range results {
			if uErr := storer.UpsertTranslation(ctx, it.EntityType, it.EntityID, it.Field, lang, txt, hash); uErr != nil {
				log.Warn().Err(uErr).Str("entity", it.EntityType).Int64("id", it.EntityID).
					Str("field", it.Field).Str("lang", lang).Msg("failed to store translation")
				continue
			}
			filled++
			// Backward-compat bridge: keep the legacy _en columns the API/frontend
			// still read in sync with the generic store (best-effort).
			if lang == "en" {
				if lErr := storer.UpdateLegacyEnColumn(ctx, it.EntityType, it.Field, it.EntityID, txt); lErr != nil {
					log.Warn().Err(lErr).Str("entity", it.EntityType).Int64("id", it.EntityID).Msg("failed to mirror EN into legacy column")
				}
			}
		}
	}
	return filled, len(items), nil
}

// runTranslateStep is the daily pipeline's final translation phase. Best-effort:
// it must never block or abort the pipeline (generation has already completed).
func runTranslateStep(ctx context.Context, storer *storage.PostgresStorer) error {
	tr, err := translate.NewOpenAITranslatorFromEnv()
	if err != nil {
		return fmt.Errorf("translator unavailable: %w", err)
	}
	// Cap per run so the first post-deploy run doesn't try to translate the entire
	// backlog (thousands of facts) in one pass — it drains incrementally over runs.
	filled, scanned, err := runTranslateFill(ctx, storer, tr, translate.DefaultTargetLangs, nil, dailyTranslateLimit)
	if err != nil {
		return err
	}
	log.Info().Int("filled", filled).Int("items", scanned).Msg("Daily pipeline: translation phase done")
	return nil
}

// dailyTranslateLimit caps how many translations the daily pipeline's final phase
// fills per run, so a large first-time backlog drains over several runs instead of
// blocking one run for hours.
const dailyTranslateLimit = 1500
