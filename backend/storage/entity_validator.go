package storage

import (
	"context"
	"errors"
	"fmt"

	"digital-mirror/backend/entityvalidator"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// pgxErrNoRows is a local alias so we don't have to import pgx in every helper.
var pgxErrNoRows = pgx.ErrNoRows

// EntityValidator is the storage-layer abstraction over an entity-validation
// pipeline. Storage methods that create new persons consult this interface
// before issuing the INSERT. We use an interface (not a direct dependency on
// *entityvalidator.Validator) so tests can swap in stubs and so users without
// LLM/network access can construct a storer with validation disabled.
type EntityValidator interface {
	Validate(ctx context.Context, name, entityType string) entityvalidator.Verdict
}

// SetEntityValidator attaches a validator that will be consulted before every
// new-person INSERT. Pass nil to disable validation entirely. Existing persons
// (resolved by name lookup) are never re-validated — only fresh inserts.
func (s *PostgresStorer) SetEntityValidator(v EntityValidator) {
	s.validator = v
}

// ErrEntityRejected is an alias for entityvalidator.RejectedError, exposed at
// the storage layer for ergonomic call sites. The real type lives in the
// validator package so any layer can reference it without import cycles.
type ErrEntityRejected = entityvalidator.RejectedError

// IsEntityRejected reports whether err (or any wrapped error) is an entity
// rejection. Delegates to entityvalidator.IsRejected.
func IsEntityRejected(err error) bool { return entityvalidator.IsRejected(err) }

// DeletePersonByID removes a single persons row. Used by the
// `dm entities validate --apply` CLI. Returns an error if the delete fails for
// any reason (e.g. lingering FK references) — the CLI surfaces this per-row so
// one bad delete doesn't abort the rest of the cleanup batch.
func (s *PostgresStorer) DeletePersonByID(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM persons WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeletePersonByID(%d): %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("DeletePersonByID(%d): no row deleted", id)
	}
	return nil
}

// GetFactCountsByPersonIDs returns a map (person_id → count) for the supplied
// IDs, sourced from the stored persons.fact_count counter (not a JOIN on facts,
// which would be O(facts) per id). IDs with zero facts may be absent from the
// returned map.
func (s *PostgresStorer) GetFactCountsByPersonIDs(ctx context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(fact_count, 0) FROM persons WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("GetFactCountsByPersonIDs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out, rows.Err()
}

// findPersonIDByName returns the existing person id for a normalized name,
// or (0, nil) if no row exists. Used by the *Add* methods to skip validation
// for already-known persons (only NEW candidates need the wiki/LLM check).
func (s *PostgresStorer) findPersonIDByName(ctx context.Context, normalizedName string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM persons WHERE name = $1`, normalizedName).Scan(&id)
	if err != nil {
		if errors.Is(err, pgxErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}

// checkValidator runs the configured validator for a NEW candidate. Returns
// nil to proceed with insertion, or *ErrEntityRejected to abort it. A nil
// validator (validation disabled) always returns nil.
//
// The result is always logged at INFO so the daily pipeline leaves an audit
// trail of every entity-creation decision.
func (s *PostgresStorer) checkValidator(ctx context.Context, name, entityType string) error {
	if s.validator == nil {
		return nil
	}
	v := s.validator.Validate(ctx, name, entityType)
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
	return &entityvalidator.RejectedError{
		Name:       name,
		EntityType: entityType,
		Filter:     v.Filter,
		Reason:     v.Reason,
	}
}
