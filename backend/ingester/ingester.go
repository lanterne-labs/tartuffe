package ingester

import (
	"context"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"fmt"
)

// FactIngester turns normalized facts into DB rows and triggers contradiction checks.
type FactIngester struct {
	Embedder embedding.EmbeddingGenerator
	Store    *storage.PostgresStorer
}

// Ingest embeds given normalized fact texts (and required fields) and saves them.
// Contradiction checks are intentionally not performed here to keep concerns separated.
func (i *FactIngester) Ingest(ctx context.Context, items []storage.StoreFactInput) error {
	if len(items) == 0 {
		return nil
	}
	texts := make([]string, len(items))
	for idx, it := range items {
		texts[idx] = it.NormalizedFactText
	}
	vecs, err := i.Embedder.GenerateEmbeddings(ctx, texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(items) {
		return fmt.Errorf("embedding count mismatch: %d vs %d", len(vecs), len(items))
	}
	for idx, it := range items {
		it.Embedding = vecs[idx]
		if _, _, _, err := i.Store.StoreFact(ctx, it); err != nil {
			return err
		}
	}
	return nil
}
