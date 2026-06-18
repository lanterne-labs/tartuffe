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

// InsertStanceDocument inserts a new stance document and returns its ID.
func (a *StanceStorerAdapter) InsertStanceDocument(ctx context.Context, doc core.StanceDocument) (int64, error) {
	storageDoc := StanceDocument{
		PersonID:         doc.PersonID,
		ClusterLabel:     doc.ClusterLabel,
		LabelEn:          doc.LabelEn,
		ClusterEmbedding: doc.ClusterEmbedding,
		Summary:          doc.Summary,
		SummaryEn:        doc.SummaryEn,
		KeyFactIDs:       doc.KeyFactIDs,
		FactCount:        doc.FactCount,
	}
	return a.storer.InsertStanceDocument(ctx, storageDoc)
}

// UpdateStanceDocument updates an existing stance document.
func (a *StanceStorerAdapter) UpdateStanceDocument(ctx context.Context, id int64, summary, summaryEn string, embedding []float32, keyFactIDs []int64, factCount int) error {
	return a.storer.UpdateStanceDocument(ctx, id, summary, summaryEn, embedding, keyFactIDs, factCount)
}

// UpdateStanceMetadata updates ONLY the cluster_label/label_en and summary/summary_en of a stance.
// Used by the per-fact stance-refresh path (ProcessNewFact → handleVerdict →
// triggerStanceRefresh) to keep the stance's title and description in sync
// with its accumulating fact set.
func (a *StanceStorerAdapter) UpdateStanceMetadata(ctx context.Context, id int64, label, labelEn, summary, summaryEn string) error {
	return a.storer.UpdateStanceMetadata(ctx, id, label, labelEn, summary, summaryEn)
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
			LabelEn:          d.LabelEn,
			ClusterEmbedding: d.ClusterEmbedding,
			Summary:          d.Summary,
			SummaryEn:        d.SummaryEn,
			KeyFactIDs:       d.KeyFactIDs,
			FactCount:        d.FactCount,
		}
	}
	return result, nil
}

// ShortlistStancesByNearestFact ranks a person's stances by their nearest member
// fact to factEmbedding and returns the top-K, each with its nearest member fact
// texts. Converts storage.StanceCandidate to core.StanceCandidate.
func (a *StanceStorerAdapter) ShortlistStancesByNearestFact(ctx context.Context, personID int64, factEmbedding []float32, topK, factsPerStance int) ([]core.StanceCandidate, error) {
	cands, err := a.storer.ShortlistStancesByNearestFact(ctx, personID, factEmbedding, topK, factsPerStance)
	if err != nil {
		return nil, err
	}

	result := make([]core.StanceCandidate, len(cands))
	for i, c := range cands {
		result[i] = core.StanceCandidate{
			ID:          c.ID,
			Label:       c.Label,
			Summary:     c.Summary,
			FactCount:   c.FactCount,
			MemberFacts: c.MemberFacts,
			NearestDist: c.NearestDist,
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
			Lang:               f.Lang,
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

// RecordContradiction records a contradiction between two facts. Drops the bundler-needed
// signal because in this code path (StanceManager.ProcessNewFact) both facts are guaranteed
// to be in the same stance, so the SQL fast path will always set bundle_stance_id directly.
func (a *StanceStorerAdapter) RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) error {
	_, _, err := a.storer.RecordContradictionImmediate(ctx, personID, factAID, factBID, reason, reasonEn)
	return err
}
