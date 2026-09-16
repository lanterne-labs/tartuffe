package core

import (
	"context"
	"digital-mirror/backend/embedding"
	"fmt"

	"github.com/rs/zerolog/log"
)

// EmbeddingRegenStorer is the storage view RegenerateEmbeddings needs.
// *storage.PostgresStorer implements it.
type EmbeddingRegenStorer interface {
	CountFactsWithoutEmbedding(ctx context.Context) (int64, error)
	// ListFactsWithoutEmbedding returns up to limit facts whose embedding is
	// NULL, oldest first, with the text to embed.
	ListFactsWithoutEmbedding(ctx context.Context, limit int) ([]FactEmbeddingText, error)
	UpdateFactEmbedding(ctx context.Context, factID int64, embedding []float32) error
}

// EmbeddingRegenStats are the counters of a RegenerateEmbeddings pass.
type EmbeddingRegenStats struct {
	Total     int64 // facts without an embedding when the pass started
	Processed int64
	Failed    int64
}

// RegenerateEmbeddings walks every fact whose embedding is NULL and refills it,
// batchSize facts per embedding request. Idempotent and resumable: each row is
// committed on its own, so re-running picks up where a killed run left off. A
// failed embedding request aborts the pass (the model or endpoint is wrong, not
// one row); a failed row update is logged and counted.
func RegenerateEmbeddings(ctx context.Context, storer EmbeddingRegenStorer, embedder embedding.EmbeddingGenerator, batchSize int) (EmbeddingRegenStats, error) {
	var stats EmbeddingRegenStats
	if batchSize <= 0 {
		return stats, fmt.Errorf("batch size must be > 0")
	}

	total, err := storer.CountFactsWithoutEmbedding(ctx)
	if err != nil {
		return stats, err
	}
	stats.Total = total
	if total == 0 {
		return stats, nil
	}

	batchNum := 0
	for {
		facts, err := storer.ListFactsWithoutEmbedding(ctx, batchSize)
		if err != nil {
			return stats, err
		}
		if len(facts) == 0 {
			break
		}
		batchNum++

		texts := make([]string, len(facts))
		for i, f := range facts {
			texts[i] = f.EmbeddingText
		}
		vecs, err := embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
		if err != nil {
			log.Error().Err(err).Int("batch", batchNum).Int("size", len(facts)).Msg("Failed to generate embeddings for batch, aborting")
			return stats, fmt.Errorf("generate embeddings (batch %d): %w", batchNum, err)
		}
		if len(vecs) != len(facts) {
			return stats, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(facts), len(vecs))
		}

		for i, f := range facts {
			if err := storer.UpdateFactEmbedding(ctx, f.ID, vecs[i]); err != nil {
				log.Error().Err(err).Int64("fact_id", f.ID).Msg("Failed to update fact embedding")
				stats.Failed++
				continue
			}
			stats.Processed++
		}

		pct := float64(stats.Processed) / float64(total) * 100
		log.Info().
			Int("batch", batchNum).
			Int64("processed", stats.Processed).
			Int64("failed", stats.Failed).
			Int64("total", total).
			Float64("pct", pct).
			Msg("Regenerate progress")
	}
	return stats, nil
}
