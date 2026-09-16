package storage

import (
	"context"
	"fmt"
)

// CreateAffiliation records (idempotently) that person srcID is affiliated
// with party dstID, with no validity window and no attributes, in its own
// transaction.
func (s *PostgresStorer) CreateAffiliation(ctx context.Context, srcID, dstID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := s.FindOrCreateAffiliation(ctx, tx, srcID, dstID, nil, nil, map[string]any{}); err != nil {
		tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
