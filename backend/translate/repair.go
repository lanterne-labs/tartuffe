package translate

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// RepairStorer is the storage surface the English repair pass uses.
type RepairStorer interface {
	GetFactsMissingEnglish(ctx context.Context, limit int) ([]storage.FactNeedingTranslation, error)
	SetFactEnglish(ctx context.Context, factID int64, english string) error
}

// RepairMissingEnglish fills fact_text_en for up to limit non-English facts
// that are missing it. Returns (filled, failed, err). A per-fact LLM failure
// is logged and counted but does not abort the batch; the next run retries
// it. The translator is only built once there is something to translate.
func RepairMissingEnglish(ctx context.Context, storer RepairStorer, limit int) (int, int, error) {
	facts, err := storer.GetFactsMissingEnglish(ctx, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to list facts missing english: %w", err)
	}
	if len(facts) == 0 {
		log.Info().Msg("Translation repair: no facts missing English, nothing to do")
		return 0, 0, nil
	}

	translator, err := core.NewTranslatorFromEnv()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create translator: %w", err)
	}

	log.Info().Int("count", len(facts)).Msg("Translation repair: filling missing English translations")
	var filled, failed int
	for _, f := range facts {
		en, tErr := translator.TranslateToEnglish(ctx, f.Text, f.Lang)
		if tErr != nil {
			failed++
			log.Warn().Err(tErr).Int64("fact_id", f.ID).Msg("Translation repair: translate failed, leaving for next run")
			continue
		}
		if sErr := storer.SetFactEnglish(ctx, f.ID, en); sErr != nil {
			failed++
			log.Error().Err(sErr).Int64("fact_id", f.ID).Msg("Translation repair: failed to persist translation")
			continue
		}
		filled++
	}
	log.Info().Int("filled", filled).Int("failed", failed).Msg("Translation repair complete")
	return filled, failed, nil
}
