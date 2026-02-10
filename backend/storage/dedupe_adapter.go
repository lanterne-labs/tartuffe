package storage

import (
	"context"
	"digital-mirror/backend/core"
)

// DedupeStorerAdapter adapts PostgresStorer to implement core.DedupeStorer.
// This allows the deduplicator to use the storage layer without direct dependencies.
type DedupeStorerAdapter struct {
	storer *PostgresStorer
}

// NewDedupeStorerAdapter creates a new adapter for the given PostgresStorer.
func NewDedupeStorerAdapter(storer *PostgresStorer) *DedupeStorerAdapter {
	return &DedupeStorerAdapter{storer: storer}
}

// GetAllPersons retrieves all persons from the database.
func (a *DedupeStorerAdapter) GetAllPersons(ctx context.Context) ([]core.DedupePersonInfo, error) {
	persons, err := a.storer.GetAllPersons(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]core.DedupePersonInfo, len(persons))
	for i, p := range persons {
		result[i] = core.DedupePersonInfo{
			ID:         p.ID,
			Name:       p.Name,
			EntityType: p.EntityType,
		}
	}
	return result, nil
}

// GetPersonIDByName returns the person ID for a given name.
func (a *DedupeStorerAdapter) GetPersonIDByName(ctx context.Context, name string) (int64, error) {
	return a.storer.GetPersonIDByName(ctx, name)
}

// GetFactsForDeduplication retrieves facts for a person with their embeddings and text.
func (a *DedupeStorerAdapter) GetFactsForDeduplication(ctx context.Context, personID int64) ([]core.FactForDedup, error) {
	facts, err := a.storer.GetFactsForDeduplication(ctx, personID)
	if err != nil {
		return nil, err
	}

	result := make([]core.FactForDedup, len(facts))
	for i, f := range facts {
		result[i] = core.FactForDedup{
			ID:                 f.ID,
			NormalizedFactText: f.NormalizedFactText,
			EmbeddingText:      f.EmbeddingText,
			OriginalQuote:      f.OriginalQuote,
			Embedding:          f.Embedding,
			IsCanonical:        f.IsCanonical, // Now means "is active" (not a duplicate)
		}
	}
	return result, nil
}

// FindSimilarFactsForDedup finds facts similar to a given embedding within a threshold.
func (a *DedupeStorerAdapter) FindSimilarFactsForDedup(ctx context.Context, personIDs []int64, embedding []float32, excludeFactID int64, threshold float64) ([]core.SimilarFactResult, error) {
	facts, err := a.storer.FindSimilarFactsForDedup(ctx, personIDs, embedding, excludeFactID, threshold)
	if err != nil {
		return nil, err
	}

	result := make([]core.SimilarFactResult, len(facts))
	for i, f := range facts {
		result[i] = core.SimilarFactResult{
			ID:         f.ID,
			Text:       f.Text,
			Distance:   f.Distance,
			Similarity: f.Similarity,
		}
	}
	return result, nil
}

// MarkFactAsDuplicate marks a fact as a duplicate of a canonical fact
// by inserting a 'duplicate' relation into fact_relations.
func (a *DedupeStorerAdapter) MarkFactAsDuplicate(ctx context.Context, duplicateID int64, canonicalID int64) error {
	return a.storer.MarkFactAsDuplicate(ctx, duplicateID, canonicalID)
}

// GetFactByID retrieves a single fact by its ID.
func (a *DedupeStorerAdapter) GetFactByID(ctx context.Context, factID int64) (*core.FactForDedup, error) {
	fact, err := a.storer.GetFactByID(ctx, factID)
	if err != nil {
		return nil, err
	}

	return &core.FactForDedup{
		ID:                 fact.ID,
		NormalizedFactText: fact.NormalizedFactText,
		EmbeddingText:      fact.EmbeddingText,
		OriginalQuote:      fact.OriginalQuote,
		Embedding:          fact.Embedding,
		IsCanonical:        fact.IsCanonical, // Now means "is active" (not a duplicate)
	}, nil
}

// FindOrInsertPersons resolves person names to person IDs.
func (a *DedupeStorerAdapter) FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error) {
	return a.storer.FindOrInsertPersons(ctx, names)
}

// UpdateFactText updates a fact's normalized text and embedding text.
func (a *DedupeStorerAdapter) UpdateFactText(ctx context.Context, factID int64, normalizedFact string, embeddingText string) error {
	return a.storer.UpdateFactText(ctx, factID, normalizedFact, embeddingText)
}

// UpdateFactEmbedding updates a fact's embedding vector.
func (a *DedupeStorerAdapter) UpdateFactEmbedding(ctx context.Context, factID int64, embedding []float32) error {
	return a.storer.UpdateFactEmbedding(ctx, factID, embedding)
}

// DeleteFact deletes a fact that has been determined to be invalid.
func (a *DedupeStorerAdapter) DeleteFact(ctx context.Context, factID int64) error {
	return a.storer.DeleteFact(ctx, factID)
}
