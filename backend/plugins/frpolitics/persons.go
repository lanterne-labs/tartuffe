package frpolitics

import (
	"context"
	"digital-mirror/backend/util"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// BatchInsertActors inserts all actors (deputies and parties) in batch operations within a transaction.
func BatchInsertActors(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, deputies map[string]string, parties map[string]PartyMeta) error {
	// Batch insert deputies
	if len(deputies) > 0 {
		values := make([]interface{}, 0, len(deputies)*3)
		placeholders := make([]string, 0, len(deputies))
		idx := 1
		for id, name := range deputies {
			if name == "" {
				continue
			}
			norm := util.NormalizePersonName(name)
			placeholders = append(placeholders, fmt.Sprintf("($%d::text, 'human', jsonb_build_object('assemblee_id', $%d::text, 'constituency', $%d::text))", idx, idx+1, idx+2))
			values = append(values, norm, id, "")
			idx += 3
		}
		if len(placeholders) > 0 {
			stmt := fmt.Sprintf(`
				INSERT INTO persons (name, entity_type, attributes)
				VALUES %s
				ON CONFLICT (name) DO UPDATE SET
				  entity_type = 'human',
				  attributes = persons.attributes || jsonb_build_object(
					'assemblee_id', EXCLUDED.attributes->>'assemblee_id',
					'constituency', EXCLUDED.attributes->>'constituency'
				  )
			`, strings.Join(placeholders, ", "))
			_, err := tx.Exec(ctx, stmt, values...)
			if err != nil {
				return fmt.Errorf("batch insert deputies: %w", err)
			}
		}
	}

	// Batch insert parties
	if len(parties) > 0 {
		values := make([]interface{}, 0, len(parties)*3)
		placeholders := make([]string, 0, len(parties))
		idx := 1
		for name, meta := range parties {
			if name == "" {
				continue
			}
			norm := util.NormalizePersonName(name)
			placeholders = append(placeholders, fmt.Sprintf("($%d::text, 'party', jsonb_build_object('organeRef', $%d::text, 'abbrev', $%d::text))", idx, idx+1, idx+2))
			values = append(values, norm, meta.OrgRef, meta.Abbrev)
			idx += 3
		}
		if len(placeholders) > 0 {
			stmt := fmt.Sprintf(`
				INSERT INTO persons (name, entity_type, attributes)
				VALUES %s
				ON CONFLICT (name) DO UPDATE SET
				  entity_type = 'party',
				  attributes = persons.attributes || jsonb_build_object(
					'organeRef', EXCLUDED.attributes->>'organeRef',
					'abbrev', EXCLUDED.attributes->>'abbrev'
				  )
			`, strings.Join(placeholders, ", "))
			_, err := tx.Exec(ctx, stmt, values...)
			if err != nil {
				return fmt.Errorf("batch insert parties: %w", err)
			}
		}
	}

	return nil
}

// PartyMeta holds party metadata
type PartyMeta struct {
	OrgRef string
	Abbrev string
}
