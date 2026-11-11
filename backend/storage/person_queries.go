package storage

import (
	"context"
	"digital-mirror/backend/util"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// personIDByFoldedName returns the id of the person whose name folds to the
// same key as name, or (0, nil) when none does. It is the one name lookup
// behind every person insert path and GetPersonIDByName.
//
// Both sides are folded in SQL with lower(immutable_unaccent(...)): the column
// side is served by idx_persons_name_normkey, and folding the argument as well
// covers the folds Postgres unaccent applies but a Go fold does not (ß→ss,
// æ→ae, œ→oe, ł→l, …), so "łukasz" resolves to a stored "lukasz" and vice
// versa. Callers pass a util.NormalizePersonName'd input all the same: that
// fold also collapses typographic punctuation and whitespace, which unaccent
// does not, and since the SQL fold is idempotent every match the older
// column-only form found is still found.
//
// When no name folds to the same key, a name with the same separator-free key
// (person_name_key, util.PersonNameKey) is the person: "olivier fa-ure" is
// "olivier faure". Among several, the one with the most facts.
func personIDByFoldedName(ctx context.Context, q rowQueryer, name string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1)) LIMIT 1`,
		name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("failed to look up person %q by name: %w", name, err)
	}
	if util.PersonNameKey(name) == "" {
		return 0, nil
	}
	err = q.QueryRow(ctx,
		`SELECT id FROM persons WHERE person_name_key(name) = person_name_key($1) ORDER BY fact_count DESC, id LIMIT 1`,
		name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to look up person %q by name key: %w", name, err)
	}
	return id, nil
}

// resolveOrInsertPerson is the lookup → gate → insert core shared by every
// person entry point. name must be util.NormalizePersonName'd and non-empty.
// An existing row (matched by personIDByFoldedName) is returned as is,
// inserted=false, without consulting the gate. A new candidate reaches the
// gate as (name, entityType), "" meaning unknown, and if allowed is stored
// with entityType, or the column default (human) when "".
func (s *PostgresStorer) resolveOrInsertPerson(ctx context.Context, q rowQueryer, name, entityType string) (id int64, inserted bool, err error) {
	id, err = personIDByFoldedName(ctx, q, name)
	if err != nil || id != 0 {
		return id, false, err
	}
	if err := s.checkPersonGate(ctx, name, entityType); err != nil {
		return 0, false, err
	}
	storedType := entityType
	if storedType == "" {
		storedType = "human"
	}
	err = q.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ($1, $2)
		 ON CONFLICT (name) DO NOTHING
		 RETURNING id`,
		name, storedType).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Exact-name race: a concurrent writer inserted the row between the
		// lookup and the insert. Resolve it by fold like the lookup above.
		id, err = personIDByFoldedName(ctx, q, name)
		if err != nil {
			return 0, false, err
		}
		if id == 0 {
			return 0, false, fmt.Errorf("person %q conflicted on insert but no row folds to it", name)
		}
		return id, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("failed to insert person %q: %w", name, err)
	}
	log.Info().Int64("person_id", id).Str("name", name).Str("entity_type", storedType).Msg("Created new person")
	return id, true, nil
}

// PersonSummaryColumns is the persons projection every PersonSummary query
// selects (alias p), in scanPersonSummary order; each query appends its own
// count columns after it.
// PersonSummaryColumns is the persons projection every PersonSummary reader
// selects (alias p); scan it with ScanPersonSummary.
const PersonSummaryColumns = `p.id, p.slug, p.name, p.entity_type, p.honesty_score, p.own_score, p.members_score, p.image_url, ` + CandidateSQL

// ScanPersonSummary scans PersonSummaryColumns, then extra, into person.
func ScanPersonSummary(row pgx.Row, person *PersonSummary, extra ...any) error {
	dest := append([]any{&person.ID, &person.Slug, &person.Name, &person.EntityType, &person.HonestyScore, &person.OwnScore, &person.MembersScore, &person.ImageURL, &person.Candidate}, extra...)
	return row.Scan(dest...)
}
