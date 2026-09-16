package core

import (
	"context"
	"digital-mirror/backend/embedding"
	"fmt"

	"github.com/rs/zerolog/log"
)

// FactEmbeddingText is a fact id with the text its embedding is (to be)
// computed from.
type FactEmbeddingText struct {
	ID            int64
	EmbeddingText string
}

// VerbBackfillStorer is the storage view BackfillCanonicalVerbs needs.
// *storage.PostgresStorer implements it.
type VerbBackfillStorer interface {
	// ListFactsWithoutCanonicalVerb returns every fact whose canonical_verb is
	// unset, with its current embedding_text.
	ListFactsWithoutCanonicalVerb(ctx context.Context) ([]FactEmbeddingText, error)
	UpdateFactVerbAndEmbedding(ctx context.Context, factID int64, verb, embeddingText string, embedding []float32) error
}

// VerbParser splits a "verb: topic" embedding text into its parts; ok is false
// when the text carries no verb prefix. The parser lives in backend/categorizer,
// which core does not import, so the caller wires it in.
type VerbParser func(embeddingText string) (verb, topic string, ok bool)

// VerbBackfillStats are the counters of a BackfillCanonicalVerbs pass.
type VerbBackfillStats struct {
	Candidates int // facts with a parsable verb prefix
	Updated    int
}

// verbBackfillBatchSize is how many facts are re-embedded per request.
const verbBackfillBatchSize = 50

// BackfillCanonicalVerbs parses the canonical verb prefix out of existing
// embedding_text values, sets canonical_verb, strips the verb from
// embedding_text, and regenerates the embedding from the verb-free text. With
// dryRun only the preview is logged and embedder may be nil. A batch whose
// embedding fails is logged and skipped.
func BackfillCanonicalVerbs(ctx context.Context, storer VerbBackfillStorer, embedder embedding.EmbeddingGenerator, parse VerbParser, dryRun bool) (VerbBackfillStats, error) {
	var stats VerbBackfillStats

	facts, err := storer.ListFactsWithoutCanonicalVerb(ctx)
	if err != nil {
		return stats, err
	}

	type factToUpdate struct {
		ID            int64
		OldEmbedding  string
		CanonicalVerb string
		NewEmbedding  string
	}
	var updates []factToUpdate
	for _, f := range facts {
		verb, topic, ok := parse(f.EmbeddingText)
		if !ok {
			log.Debug().Int64("id", f.ID).Str("embedding_text", f.EmbeddingText).Msg("No verb prefix found, skipping")
			continue
		}
		updates = append(updates, factToUpdate{ID: f.ID, OldEmbedding: f.EmbeddingText, CanonicalVerb: verb, NewEmbedding: topic})
	}
	stats.Candidates = len(updates)

	log.Info().Int("total", len(updates)).Bool("dry_run", dryRun).Msg("Facts to backfill")

	if len(updates) == 0 {
		log.Info().Msg("No facts need backfilling")
		return stats, nil
	}

	// Show first few changes
	showCount := 10
	if len(updates) < showCount {
		showCount = len(updates)
	}
	for _, u := range updates[:showCount] {
		log.Info().
			Int64("id", u.ID).
			Str("verb", u.CanonicalVerb).
			Str("old", u.OldEmbedding).
			Str("new", u.NewEmbedding).
			Msg("Backfill preview")
	}
	if len(updates) > showCount {
		log.Info().Int("remaining", len(updates)-showCount).Msg("... and more")
	}

	if dryRun {
		log.Info().Msg("Dry run complete, no changes made")
		return stats, nil
	}
	if embedder == nil {
		return stats, fmt.Errorf("embedding generator required to backfill verbs")
	}

	for i := 0; i < len(updates); i += verbBackfillBatchSize {
		end := i + verbBackfillBatchSize
		if end > len(updates) {
			end = len(updates)
		}
		batch := updates[i:end]

		texts := make([]string, len(batch))
		for j, u := range batch {
			texts[j] = u.NewEmbedding
		}

		embeddings, err := embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
		if err != nil {
			log.Error().Err(err).Int("batch_start", i).Msg("Failed to generate embeddings for batch, skipping")
			continue
		}
		if len(embeddings) != len(batch) {
			log.Error().Int("expected", len(batch)).Int("got", len(embeddings)).Msg("Embedding count mismatch")
			continue
		}

		for j, u := range batch {
			if err := storer.UpdateFactVerbAndEmbedding(ctx, u.ID, u.CanonicalVerb, u.NewEmbedding, embeddings[j]); err != nil {
				log.Error().Err(err).Int64("id", u.ID).Msg("Failed to update fact")
				continue
			}
			stats.Updated++
		}

		log.Info().
			Int("batch", (i/verbBackfillBatchSize)+1).
			Int("updated", stats.Updated).
			Int("total", len(updates)).
			Msg("Backfill progress")
	}

	log.Info().Int("updated", stats.Updated).Int("total", len(updates)).Msg("Backfill complete")
	return stats, nil
}
