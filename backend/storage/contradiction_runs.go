package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// DetectionReader is a read-only transaction scoped to one person's candidate
// walk in contradiction detection: the KNN neighbor query and the ancestor
// text/date lookups behind it run on one snapshot instead of checking a pool
// connection out per query. Close releases it.
type DetectionReader struct {
	s  *PostgresStorer
	tx pgx.Tx
}

// BeginDetectionRead opens a DetectionReader on a fresh transaction.
func (s *PostgresStorer) BeginDetectionRead(ctx context.Context) (*DetectionReader, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin detection read transaction: %w", err)
	}
	return &DetectionReader{s: s, tx: tx}, nil
}

// Neighbors returns the person's active facts within the similarity threshold
// of embedding, excluding excludeFactID (see FindNeighborFactsForPerson).
func (r *DetectionReader) Neighbors(ctx context.Context, personID int64, embedding []float32, excludeFactID int64) ([]NeighborFactResult, error) {
	return r.s.FindNeighborFactsForPerson(ctx, r.tx, personID, embedding, excludeFactID)
}

// FactText returns the text of one fact.
func (r *DetectionReader) FactText(ctx context.Context, factID int64) (string, error) {
	return r.s.GetFactTextByID(ctx, r.tx, factID)
}

// FactDate returns the earliest publication date of one fact (nil when unknown).
func (r *DetectionReader) FactDate(ctx context.Context, factID int64) (*time.Time, error) {
	return r.s.GetFactDateByID(ctx, r.tx, factID)
}

// Close rolls the read transaction back.
func (r *DetectionReader) Close(ctx context.Context) {
	if err := r.tx.Rollback(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to roll back detection read transaction")
	}
}
