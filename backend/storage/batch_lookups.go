package storage

import (
	"context"
	"errors"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/util"

	"github.com/jackc/pgx/v5"
)

// GetPersonRecordByName returns the person whose name folds to the same key
// as name (the personIDByFoldedName predicate, through the
// idx_persons_name_normkey index); found is false when nobody matches.
// Satisfies core.PersonResolver.
func (s *PostgresStorer) GetPersonRecordByName(ctx context.Context, name string) (core.PersonRecord, bool, error) {
	var p core.PersonRecord
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, COALESCE(entity_type, 'person')
		 FROM persons
		 WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1))
		 LIMIT 1`,
		util.NormalizePersonName(name)).Scan(&p.ID, &p.Name, &p.EntityType)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.PersonRecord{}, false, nil
	}
	if err != nil {
		return core.PersonRecord{}, false, fmt.Errorf("failed to find person record by name %s: %w", name, err)
	}
	return p, true, nil
}

// GetPersonIDsByAttribute resolves persons.attributes[key] values to person
// ids in one query, the batch form of GetPersonIDByAttribute. key is a fixed
// identifier chosen by the caller, never user input. Values nobody carries are
// absent from the map; a value carried by several persons maps to the lowest id.
func (s *PostgresStorer) GetPersonIDsByAttribute(ctx context.Context, key string, values []string) (map[string]int64, error) {
	out := make(map[string]int64, len(values))
	if len(values) == 0 {
		return out, nil
	}
	query := fmt.Sprintf(
		`SELECT attributes->>'%s', id FROM persons WHERE attributes->>'%s' = ANY($1) ORDER BY id`, key, key)
	rows, err := s.pool.Query(ctx, query, values)
	if err != nil {
		return nil, fmt.Errorf("failed to find persons by %s: %w", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		var id int64
		if err := rows.Scan(&value, &id); err != nil {
			return nil, fmt.Errorf("failed to scan person by %s: %w", key, err)
		}
		if _, seen := out[value]; !seen {
			out[value] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating persons by %s: %w", key, err)
	}
	return out, nil
}

// GetPersonIDsByNames resolves names to person ids in one query, the batch
// form of GetPersonIDByName: every name is matched accent-insensitively
// through its canonical person form. The map is keyed by the names as given;
// names nobody carries are absent.
func (s *PostgresStorer) GetPersonIDsByNames(ctx context.Context, names []string) (map[string]int64, error) {
	out := make(map[string]int64, len(names))
	if len(names) == 0 {
		return out, nil
	}
	normalized := make([]string, len(names))
	for i, name := range names {
		normalized[i] = util.NormalizePersonName(name)
	}
	// Both sides folded, like personIDByFoldedName; the input is carried
	// through so the result can be keyed by the names as given. A name
	// several persons fold to maps to the lowest id.
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (n.ord) n.ord, p.id
		 FROM unnest($1::text[]) WITH ORDINALITY AS n(input, ord)
		 JOIN persons p ON lower(immutable_unaccent(p.name)) = lower(immutable_unaccent(n.input))
		 ORDER BY n.ord, p.id`, normalized)
	if err != nil {
		return nil, fmt.Errorf("failed to find persons by names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ord int
		var id int64
		if err := rows.Scan(&ord, &id); err != nil {
			return nil, fmt.Errorf("failed to scan person by name: %w", err)
		}
		out[names[ord-1]] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating persons by names: %w", err)
	}
	return out, nil
}
