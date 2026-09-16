package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// pgxErrNoRows is a local alias so we don't have to import pgx in every helper.
var pgxErrNoRows = pgx.ErrNoRows

// PersonGate decides whether a NEW person candidate (normalized name + entity
// type, "" when unknown) may be inserted. nil allows insertion; a returned
// error aborts it and is passed to the caller unchanged, so a
// *core.EntityRejectedError lets batch callers skip the item while any other
// error fails the operation. Storage never builds a gate itself: the
// entity-validation policy is wired in by the caller (see deps.OpenStorer).
type PersonGate func(ctx context.Context, name, entityType string) error

// SetPersonGate attaches the gate consulted before every new-person INSERT.
// Pass nil to allow every candidate. Existing persons (resolved by name
// lookup) are never re-checked, only fresh inserts.
func (s *PostgresStorer) SetPersonGate(g PersonGate) {
	s.personGate = g
}

// checkPersonGate runs the gate for a NEW candidate; a nil gate allows.
func (s *PostgresStorer) checkPersonGate(ctx context.Context, name, entityType string) error {
	if s.personGate == nil {
		return nil
	}
	return s.personGate(ctx, name, entityType)
}

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
// or (0, nil) if no row exists. Used by the *Add* methods to skip the gate
// for already-known persons (only NEW candidates need the check).
// Matches accent- and case-insensitively (via immutable_unaccent), like the
// other find-or-insert paths, so "eric ciotti" resolves to an existing
// "éric ciotti" row instead of inserting a near-duplicate.
func (s *PostgresStorer) findPersonIDByName(ctx context.Context, normalizedName string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1)) LIMIT 1`,
		normalizedName).Scan(&id)
	if err != nil {
		if errors.Is(err, pgxErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}
