package translate

import (
	"context"
	"fmt"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// FillStorer is the storage surface the fill pass uses.
type FillStorer interface {
	ListTranslatableItems(ctx context.Context, entityTypes ...string) ([]storage.TranslatableItem, error)
	UpsertTranslation(ctx context.Context, entityType string, entityID int64, field, lang, text, sourceHash string) error
	UpdateLegacyEnColumn(ctx context.Context, entityType, field string, entityID int64, text string) error
}

// FillOptions scopes one fill pass.
type FillOptions struct {
	// Limit caps how many translations are filled (0 = no limit).
	Limit int
	// Langs are the target languages; nil means DefaultTargetLangs.
	Langs []string
	// EntityTypes restricts the scan to these entity types (fact, stance,
	// contradiction); nil means all.
	EntityTypes []string
}

// FillStats summarises one fill pass.
type FillStats struct {
	// Filled is the number of translations stored.
	Filled int
	// Scanned is the number of translatable items examined.
	Scanned int
}

// Fill is the async translation phase shared by `dm translate run` and the
// daily pipeline. For each translatable item it fills every target language
// that is missing or stale (source_hash != current hash), best-effort per
// language: a failed translation or store is logged and skipped. It stops at
// the first call the LLM could not serve and returns an error wrapping
// llm.ErrUnavailable, and when ctx is cancelled with an error wrapping
// ctx.Err().
func Fill(ctx context.Context, storer FillStorer, tr Translator, opts FillOptions) (FillStats, error) {
	targets := opts.Langs
	if len(targets) == 0 {
		targets = DefaultTargetLangs
	}
	items, err := storer.ListTranslatableItems(ctx, opts.EntityTypes...)
	if err != nil {
		return FillStats{}, fmt.Errorf("list translatable items: %w", err)
	}
	stats := FillStats{Scanned: len(items)}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return stats, fmt.Errorf("translation fill stopped after %d translations: %w", stats.Filled, err)
		}
		if opts.Limit > 0 && stats.Filled >= opts.Limit {
			break
		}
		hash := SourceHash(it.SourceText)
		// A stored translation counts as done only if it was produced from the
		// CURRENT source text (hash match); otherwise it is stale and refilled.
		done := make(map[string]bool, len(it.Existing))
		for lang, h := range it.Existing {
			if h == hash {
				done[lang] = true
			}
		}
		results, stopped := FillMissing(ctx, tr, it.SourceText, it.SrcLang, targets, done)
		// Translations already produced are stored even when the deadline cut
		// the rest of the item.
		keep := context.WithoutCancel(ctx)
		for lang, txt := range results {
			if uErr := storer.UpsertTranslation(keep, it.EntityType, it.EntityID, it.Field, lang, txt, hash); uErr != nil {
				log.Warn().Err(uErr).Str("entity", it.EntityType).Int64("id", it.EntityID).
					Str("field", it.Field).Str("lang", lang).Msg("failed to store translation")
				continue
			}
			stats.Filled++
			// Backward-compat bridge: keep the legacy _en columns the API/frontend
			// still read in sync with the generic store (best-effort).
			if lang == "en" {
				if lErr := storer.UpdateLegacyEnColumn(keep, it.EntityType, it.Field, it.EntityID, txt); lErr != nil {
					log.Warn().Err(lErr).Str("entity", it.EntityType).Int64("id", it.EntityID).Msg("failed to mirror EN into legacy column")
				}
			}
		}
		if stopped != nil {
			return stats, fmt.Errorf("translation fill stopped after %d translations: %w", stats.Filled, stopped)
		}
	}
	log.Info().Int("filled", stats.Filled).Int("items", stats.Scanned).Msg("Translation fill done")
	return stats, nil
}
