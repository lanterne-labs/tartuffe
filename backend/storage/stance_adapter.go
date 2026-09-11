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

// UpdateStanceMetadata updates ONLY the native cluster_label and summary of a stance.
// Used by the per-fact stance-refresh path (ProcessNewFact → handleVerdict →
// refreshAfterVerdict) to keep the stance's title and description in sync
// with its accumulating fact set. Translations are filled by the async phase.
func (a *StanceStorerAdapter) UpdateStanceMetadata(ctx context.Context, id int64, label, summary string, consumedFactIDs []int64) error {
	return a.storer.UpdateStanceMetadata(ctx, id, label, summary, consumedFactIDs)
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
	return toCoreStanceFacts(facts), nil
}

// GetUnsummarizedFactsForStance retrieves the stance's facts added after its
// last metadata refresh (all of them if never refreshed).
func (a *StanceStorerAdapter) GetUnsummarizedFactsForStance(ctx context.Context, stanceID int64) ([]core.StanceFactInfo, error) {
	facts, err := a.storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	if err != nil {
		return nil, err
	}
	return toCoreStanceFacts(facts), nil
}

// GetStanceMetadata returns the stance's current native label and summary.
func (a *StanceStorerAdapter) GetStanceMetadata(ctx context.Context, stanceID int64) (string, string, error) {
	return a.storer.GetStanceMetadata(ctx, stanceID)
}

// GetStanceTensions returns the recorded contradictions between facts of the
// stance, keyed by fact id.
func (a *StanceStorerAdapter) GetStanceTensions(ctx context.Context, stanceID int64) (map[int64][]core.StanceTension, error) {
	raw, err := a.storer.GetStanceTensions(ctx, stanceID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]core.StanceTension, len(raw))
	for id, list := range raw {
		for _, t := range list {
			out[id] = append(out[id], core.StanceTension{FactID: t.FactID, OtherFactText: t.OtherFactText, Reason: t.Reason})
		}
	}
	return out, nil
}

func toCoreStanceFacts(facts []StanceFactInfo) []core.StanceFactInfo {
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
	return result
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
// to be in the same stance OF THIS PERSON, so the person-scoped SQL fast path in
// RecordContradiction always sets bundle_stance_id directly.
func (a *StanceStorerAdapter) RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) error {
	_, _, err := a.storer.RecordContradictionImmediate(ctx, personID, factAID, factBID, reason, reasonEn)
	return err
}
