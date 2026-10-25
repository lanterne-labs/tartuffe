package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/util"
	"errors"
	"fmt"
	"sync/atomic"

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

// PendingRefreshStorer lists the stances whose label and summary have not
// absorbed all their facts (see storage.ListStancesPendingRefresh).
type PendingRefreshStorer interface {
	ListStancesPendingRefresh(ctx context.Context, limit int) ([]int64, error)
}

// RefreshPendingStances refreshes the label and summary of up to budget
// stances with pending facts, and returns the number of stances it tried.
// ApplyStanceDecision leaves the refresh to this step, so a stance that got
// many facts in one run is refreshed once. An unavailable LLM or the end of
// ctx stops the step and is returned. Another failure is logged, and the
// stance keeps its previous label and summary until the next run.
func RefreshPendingStances(ctx context.Context, storer PendingRefreshStorer, sm *StanceManager, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	ids, err := storer.ListStancesPendingRefresh(ctx, budget)
	if err != nil {
		return 0, err
	}
	// The stances are independent: they run as many at once as the endpoint
	// serves.
	var tried, failed atomic.Int64
	err = llm.ForEach(ctx, ids, func(ctx context.Context, id int64) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stance refresh stopped: %w", err)
		}
		err := sm.RefreshStanceMetadata(ctx, id)
		if err != nil && (errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil) {
			return fmt.Errorf("refreshing stance %d: %w", id, err)
		}
		tried.Add(1)
		if err != nil {
			failed.Add(1)
			log.Warn().Err(err).Int64("stance_id", id).Msg("Stance refresh failed; the stance keeps its previous label and summary")
		}
		return nil
	})
	if err != nil {
		return int(tried.Load()), err
	}
	log.Info().Int("stances", len(ids)).Int64("failed", failed.Load()).Msg("Refreshed the stances with pending facts")
	return len(ids), nil
}
