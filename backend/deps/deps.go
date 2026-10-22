// Package deps wires the shared, env-configured components once per process
// and hands them to whoever needs them: plugins, pipeline steps, CLI commands.
//
// Every accessor builds its component lazily on first use and caches it, so a
// run only pays for what it touches and a misconfigured component (missing
// API key) surfaces where it is needed rather than at startup.
package deps

import (
	"context"
	"fmt"
	"sync"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/entityvalidator"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/objectstore"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/webfetch"

	"github.com/rs/zerolog/log"
)

// OpenStorer connects to the database (DATABASE_URL) and attaches the person
// gate built by PersonGateFromEnv. Every command and the daily pipeline open
// their storer through it, so each new-person INSERT in the codebase flows
// through the same entity-validation policy. Tests open
// storage.NewPostgresStorerFromEnv directly, which has no gate.
func OpenStorer(ctx context.Context) (*storage.PostgresStorer, error) {
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return nil, err
	}
	storer.SetPersonGate(PersonGateFromEnv())
	// Every request to a site we read is recorded for the Sources page.
	webfetch.SetRecorder(storage.NewFetchLog(context.Background(), storer))
	return storer, nil
}

// PersonGateFromEnv builds the storage.PersonGate backed by the default
// entity-validator chain (Wikipedia, Serper and LLM filters, whichever the env
// permits). Every decision is logged at INFO so the daily pipeline leaves an
// audit trail of each entity-creation decision; a rejection is returned as a
// *core.EntityRejectedError, which batch callers treat as a per-item skip.
func PersonGateFromEnv() storage.PersonGate {
	validator := entityvalidator.BuildFromEnv()
	return func(ctx context.Context, name, entityType string) error {
		v, err := validator.Validate(ctx, name, entityType)
		if err != nil {
			// Not a rejection: the insert fails and the fact is retried.
			return fmt.Errorf("validating new entity %q: %w", name, err)
		}
		ev := log.Info().
			Str("name", name).
			Str("entity_type", entityType).
			Str("filter", v.Filter).
			Bool("real", v.Real).
			Str("reason", v.Reason)
		if v.Real {
			ev.Msg("Entity validator: APPROVE — proceeding with insert")
			return nil
		}
		ev.Msg("Entity validator: REJECT — skipping insert")
		return &core.EntityRejectedError{
			Name:       name,
			EntityType: entityType,
			Filter:     v.Filter,
			Reason:     v.Reason,
		}
	}
}

// Deps is the container of shared components for one process.
type Deps struct {
	Storer *storage.PostgresStorer

	mu               sync.Mutex
	factExtractor    *categorizer.OpenAIExtractor
	embedder         *embedding.OpenAIEmbeddingGenerator
	stanceManager    *core.StanceManager
	contentExtractor extractor.ContentExtractor
	newsSearcher     search.NewsSearcher
	webSearcher      search.WebSearcher
	imageSearcher    search.ImageSearcher
	objectStore      objectstore.ObjectStore
}

// New returns a container bound to the given storer.
func New(storer *storage.PostgresStorer) *Deps {
	return &Deps{Storer: storer}
}

// FactExtractor is the LLM fact extractor.
func (d *Deps) FactExtractor() (*categorizer.OpenAIExtractor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.factExtractor == nil {
		fe, err := categorizer.NewOpenAIExtractorFromEnv()
		if err != nil {
			return nil, err
		}
		d.factExtractor = fe
	}
	return d.factExtractor, nil
}

// Embedder is the embedding generator.
func (d *Deps) Embedder() (*embedding.OpenAIEmbeddingGenerator, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.embedder == nil {
		e, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return nil, err
		}
		d.embedder = e
	}
	return d.embedder, nil
}

// StanceManager routes inserted facts into stances and runs the analyze judge.
func (d *Deps) StanceManager() (*core.StanceManager, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stanceManager == nil {
		sm, err := core.NewStanceManagerFromEnv(d.Storer)
		if err != nil {
			return nil, err
		}
		d.stanceManager = sm
	}
	return d.stanceManager, nil
}

// ContentExtractor is the web page scraper ("auto" provider chain wrapped in
// the concurrent batch extractor).
func (d *Deps) ContentExtractor() (extractor.ContentExtractor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.contentExtractor == nil {
		raw, err := extractor.NewExtractorFromType("auto")
		if err != nil {
			return nil, err
		}
		d.contentExtractor = extractor.NewBatchExtractorFromEnv(raw)
	}
	return d.contentExtractor, nil
}

// NewsSearcher is the news search provider chain.
func (d *Deps) NewsSearcher() (search.NewsSearcher, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.newsSearcher == nil {
		ns, err := search.NewNewsSearcherFromEnv()
		if err != nil {
			return nil, err
		}
		d.newsSearcher = ns
	}
	return d.newsSearcher, nil
}

// WebSearcher is the general web search provider chain.
func (d *Deps) WebSearcher() (search.WebSearcher, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.webSearcher == nil {
		ws, err := search.NewWebSearcherFromEnv()
		if err != nil {
			return nil, err
		}
		d.webSearcher = ws
	}
	return d.webSearcher, nil
}

// ImageSearcher is the image search provider.
func (d *Deps) ImageSearcher() (search.ImageSearcher, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.imageSearcher == nil {
		is, err := search.NewImageSearcherFromEnv()
		if err != nil {
			return nil, err
		}
		d.imageSearcher = is
	}
	return d.imageSearcher, nil
}

// ObjectStore is the R2 bucket used for uploaded assets.
func (d *Deps) ObjectStore() (objectstore.ObjectStore, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.objectStore == nil {
		os, err := objectstore.NewR2ClientFromEnv()
		if err != nil {
			return nil, err
		}
		d.objectStore = os
	}
	return d.objectStore, nil
}
