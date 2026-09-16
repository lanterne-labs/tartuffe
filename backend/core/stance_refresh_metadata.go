package core

import (
	"context"
	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
)

// StanceRefreshOptions tunes RefreshMetadataForPersons.
type StanceRefreshOptions struct {
	// OnlyNonFrench skips stances whose summary already reads as French (a
	// resume mechanism and a guard against regenerating good French summaries).
	OnlyNonFrench bool
	// Full discards the current label + summary and re-derives them from every
	// fact instead of folding in only the facts added since the last refresh.
	Full bool
}

// StanceRefreshStats are the counters of a metadata refresh pass.
type StanceRefreshStats struct {
	Refreshed int
	Skipped   int // already French (OnlyNonFrench)
	Failed    int
}

// RefreshMetadataForPersons regenerates the label + summary of every stance of
// the given persons from their current facts, WITHOUT changing fact-to-stance
// assignments. Translations are left to the translation phase. A person whose
// stances cannot be loaded is logged and skipped; a stance whose refresh fails
// keeps its previous label + summary and is counted as failed.
func (sm *StanceManager) RefreshMetadataForPersons(ctx context.Context, persons []PersonRecord, opts StanceRefreshOptions) StanceRefreshStats {
	var stats StanceRefreshStats
	refresh := sm.RefreshStanceMetadata
	if opts.Full {
		refresh = sm.RebuildStanceMetadata
	}
	for _, p := range persons {
		docs, err := sm.storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("failed to load stances; skipping")
			continue
		}
		for _, d := range docs {
			if opts.OnlyNonFrench && util.LooksFrench(d.Summary) {
				stats.Skipped++
				continue
			}
			if err := refresh(ctx, d.ID); err != nil {
				log.Warn().Err(err).Int64("stance_id", d.ID).Str("person", p.Name).Msg("refresh failed")
				stats.Failed++
				continue
			}
			stats.Refreshed++
		}
	}
	return stats
}
