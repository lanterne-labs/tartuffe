package storage

import (
	"context"
	"digital-mirror/backend/core"
)

// StanceStorerAdapter adapts PostgresStorer to implement core.StanceStorer.
type StanceStorerAdapter struct {
	storer *PostgresStorer
}

// NewStanceStorerAdapter creates a new adapter for the given PostgresStorer.
func NewStanceStorerAdapter(storer *PostgresStorer) *StanceStorerAdapter {
	return &StanceStorerAdapter{storer: storer}
}

// FindMatchingStance finds the closest stance document for a person within a distance threshold.
func (a *StanceStorerAdapter) FindMatchingStance(ctx context.Context, personID int64, embedding []float32, threshold float64) (*core.StanceMatch, error) {
	match, err := a.storer.FindMatchingStance(ctx, personID, embedding, threshold)
	if err != nil {
		return nil, err
	}
	if match == nil {
		return nil, nil
	}

	return &core.StanceMatch{
		StanceDocument: core.StanceDocument{
			ID:               match.ID,
			PersonID:         match.PersonID,
			ClusterLabel:     match.ClusterLabel,
			ClusterEmbedding: match.ClusterEmbedding,
			Summary:          match.Summary,
			KeyFactIDs:       match.KeyFactIDs,
			FactCount:        match.FactCount,
		},
		Distance: match.Distance,
	}, nil
}

// InsertStanceDocument inserts a new stance document and returns its ID.
func (a *StanceStorerAdapter) InsertStanceDocument(ctx context.Context, doc core.StanceDocument) (int64, error) {
	storageDoc := StanceDocument{
		PersonID:         doc.PersonID,
		ClusterLabel:     doc.ClusterLabel,
		ClusterEmbedding: doc.ClusterEmbedding,
		Summary:          doc.Summary,
		KeyFactIDs:       doc.KeyFactIDs,
		FactCount:        doc.FactCount,
	}
	return a.storer.InsertStanceDocument(ctx, storageDoc)
}

// UpdateStanceDocument updates an existing stance document.
func (a *StanceStorerAdapter) UpdateStanceDocument(ctx context.Context, id int64, summary string, embedding []float32, keyFactIDs []int64, factCount int) error {
	return a.storer.UpdateStanceDocument(ctx, id, summary, embedding, keyFactIDs, factCount)
}

// AddFactToStance adds a fact to the stance document join table.
func (a *StanceStorerAdapter) AddFactToStance(ctx context.Context, stanceID int64, factID int64) error {
	return a.storer.AddFactToStance(ctx, stanceID, factID)
}

// GetStanceDocumentsForPerson lists all stance documents for a person.
func (a *StanceStorerAdapter) GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]core.StanceDocument, error) {
	docs, err := a.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return nil, err
	}

	result := make([]core.StanceDocument, len(docs))
	for i, d := range docs {
		result[i] = core.StanceDocument{
			ID:               d.ID,
			PersonID:         d.PersonID,
			ClusterLabel:     d.ClusterLabel,
			ClusterEmbedding: d.ClusterEmbedding,
			Summary:          d.Summary,
			KeyFactIDs:       d.KeyFactIDs,
			FactCount:        d.FactCount,
		}
	}
	return result, nil
}

// GetFactsForStance retrieves all facts associated with a stance document.
func (a *StanceStorerAdapter) GetFactsForStance(ctx context.Context, stanceID int64) ([]core.StanceFactInfo, error) {
	facts, err := a.storer.GetFactsForStance(ctx, stanceID)
	if err != nil {
		return nil, err
	}

	result := make([]core.StanceFactInfo, len(facts))
	for i, f := range facts {
		result[i] = core.StanceFactInfo{
			ID:                 f.ID,
			NormalizedFactText: f.NormalizedFactText,
			EmbeddingText:      f.EmbeddingText,
			CanonicalVerb:      f.CanonicalVerb,
		}
	}
	return result, nil
}

// GetStanceDocumentCounts returns aggregate stats.
func (a *StanceStorerAdapter) GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error) {
	return a.storer.GetStanceDocumentCounts(ctx)
}

// DeleteStanceDocumentsForPerson deletes all stance documents for a person.
func (a *StanceStorerAdapter) DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error {
	return a.storer.DeleteStanceDocumentsForPerson(ctx, personID)
}

// RecordContradiction records a contradiction between two facts.
func (a *StanceStorerAdapter) RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason string) error {
	return a.storer.RecordContradictionImmediate(ctx, personID, factAID, factBID, reason)
}
