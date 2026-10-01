package photos

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Plugin is the profile-photo source: image search plus object-store upload
// for persons and parties without a picture.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "photos" }

// NewProviderFromDeps builds a Provider on the shared components. Returns an
// error when image search or the object store is not configured.
func NewProviderFromDeps(d *deps.Deps) (*Provider, error) {
	searcher, err := d.ImageSearcher()
	if err != nil {
		return nil, fmt.Errorf("image search: %w", err)
	}
	store, err := d.ObjectStore()
	if err != nil {
		return nil, fmt.Errorf("object store: %w", err)
	}
	return &Provider{ImageSearch: searcher, R2: store, Storer: NewStore(d.Storer)}, nil
}

// RunStats counts the outcomes of a RunForPersons pass.
type RunStats struct {
	Uploaded, Skipped, Errors int
}

// RunForPersons searches and uploads a photo for each person and reports the
// counts. Per-person failures are logged and counted, never returned; a
// cancelled ctx stops it before the next person with an error wrapping
// ctx.Err().
func RunForPersons(ctx context.Context, p *Provider, persons []storage.PersonSummary, opts Options) (RunStats, error) {
	var st RunStats
	for i, person := range persons {
		if err := ctx.Err(); err != nil {
			return st, fmt.Errorf("photos stopped after %d persons: %w", i, err)
		}
		log.Info().Int("progress", i+1).Int("total", len(persons)).Str("person", person.Name).Msg("photos: person")
		result := p.SearchAndUpload(ctx, person, opts)
		switch {
		case result.Error != nil:
			log.Error().Err(result.Error).Str("person", person.Name).Msg("photos: search failed")
			st.Errors++
		case result.Skipped:
			log.Info().Str("person", person.Name).Str("reason", result.SkipReason).Msg("photos: skipped")
			st.Skipped++
		case result.Uploaded:
			log.Info().Str("person", person.Name).Str("url", result.ImageURL).Msg("photos: uploaded")
			st.Uploaded++
		}
	}
	log.Info().Int("total", len(persons)).Int("uploaded", st.Uploaded).Int("skipped", st.Skipped).Int("errors", st.Errors).
		Msg("photos: run completed")
	return st, nil
}

// RecordJobMetrics persists the pass counters under jobType.
func RecordJobMetrics(ctx context.Context, storer *storage.PostgresStorer, jobType string, st RunStats) error {
	return storer.RecordJobMetrics(ctx, []storage.JobMetric{
		{JobType: jobType, Metric: "uploaded", Value: float64(st.Uploaded)},
		{JobType: jobType, Metric: "skipped", Value: float64(st.Skipped)},
		{JobType: jobType, Metric: "errors", Value: float64(st.Errors)},
	})
}

// Backfill implements plugins.Backfiller: gives a photo to every person and
// party still missing one. A missing image-search or object-store
// configuration is a degraded but non-fatal state.
func (Plugin) Backfill(ctx context.Context, d *deps.Deps, _ time.Time) error {
	persons, err := NewStore(d.Storer).GetPersonsWithoutImage(ctx)
	if err != nil {
		return fmt.Errorf("persons without image: %w", err)
	}
	if len(persons) == 0 {
		log.Info().Msg("photos: every person has a photo, nothing to backfill")
		return nil
	}
	provider, err := NewProviderFromDeps(d)
	if err != nil {
		log.Warn().Err(err).Msg("photos: not configured, skipping backfill")
		return nil
	}
	log.Info().Int("persons", len(persons)).Msg("photos: backfill started")
	st, err := RunForPersons(ctx, provider, persons, Options{})
	if err != nil {
		return err
	}
	if err := RecordJobMetrics(ctx, d.Storer, "photos-backfill", st); err != nil {
		log.Error().Err(err).Msg("photos: failed to record backfill job metrics")
	}
	return nil
}
