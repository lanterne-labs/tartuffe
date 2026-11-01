package storage

import (
	"context"
	"encoding/json"
	"fmt"
)

// TranslatableItem is one (entity, field) whose source text may need translating
// into the configured target languages, together with the translations already
// stored (lang -> the source_hash they were produced from, for staleness checks).
type TranslatableItem struct {
	EntityType string
	EntityID   int64
	Field      string
	SourceText string
	SrcLang    string // "auto" when the entity has no language column
	Existing   map[string]string
}

// UpsertTranslation stores (or refreshes) one translation row in the generic
// translations table.
func (s *PostgresStorer) UpsertTranslation(ctx context.Context, entityType string, entityID int64, field, lang, text, sourceHash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO translations (entity_type, entity_id, field, lang, text, source_hash, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (entity_type, entity_id, field, lang)
		DO UPDATE SET text = EXCLUDED.text, source_hash = EXCLUDED.source_hash, updated_at = now()`,
		entityType, entityID, field, lang, text, sourceHash)
	if err != nil {
		return fmt.Errorf("upsert translation (%s/%d/%s/%s): %w", entityType, entityID, field, lang, err)
	}
	return nil
}

// UpdateLegacyEnColumn mirrors an English translation back into the entity's
// legacy _en column (facts.fact_text_en, stance_documents.label_en/summary_en,
// contradictions.reason_en). This is the backward-compat bridge: the API and
// frontend still read those columns, so the async translate phase keeps them
// fresh once generation stops emitting _en inline. No-op for fields without a
// legacy column. Remove once the read path reads the translations table directly.
func (s *PostgresStorer) UpdateLegacyEnColumn(ctx context.Context, entityType, field string, entityID int64, text string) error {
	var q string
	switch entityType + "/" + field {
	case "fact/text":
		q = `UPDATE facts SET fact_text_en = $1 WHERE id = $2`
	case "stance/label":
		q = `UPDATE stance_documents SET label_en = $1 WHERE id = $2`
	case "stance/summary":
		q = `UPDATE stance_documents SET summary_en = $1 WHERE id = $2`
	case "contradiction/reason":
		q = `UPDATE contradictions SET reason_en = $1 WHERE id = $2`
	default:
		return nil // no legacy column for this (entity, field)
	}
	if _, err := s.pool.Exec(ctx, q, text, entityID); err != nil {
		return fmt.Errorf("update legacy _en (%s/%s/%d): %w", entityType, field, entityID, err)
	}
	return nil
}

// ListTranslatableItems returns every translatable (entity, field) across facts,
// stances (label + summary) and contradictions (reason), each with the
// translations already present. The async translate phase decides per item which
// target languages are missing or stale. Adding a new translatable field is one
// entry in the specs slice below. When entityTypes is non-empty, only those
// entity types are scanned (e.g. "stance" to backfill just stances).
func (s *PostgresStorer) ListTranslatableItems(ctx context.Context, entityTypes ...string) ([]TranslatableItem, error) {
	want := map[string]bool{}
	for _, et := range entityTypes {
		want[et] = true
	}
	// personSQL is the condition that the row belongs to a candidate (the
	// translations are made for the candidates only, candidates.go).
	specs := []struct {
		entityType, field, table, textCol, srcLangExpr, personSQL string
	}{
		{"fact", "text", "facts", "fact_text", "coalesce(e.lang, '')",
			`EXISTS (SELECT 1 FROM fact_sources fs JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			         JOIN persons p ON p.id = fm.person_id WHERE fs.fact_id = e.id AND ` + CandidateSQL + `)`},
		// Stance label/summary are pinned to French (refreshStanceMetadata), so the
		// source is 'fr' — the translate phase then only needs to fill the other
		// languages (no redundant fr=native row).
		{"stance", "label", "stance_documents", "cluster_label", "'fr'", byPersonID},
		{"stance", "summary", "stance_documents", "summary", "'fr'", byPersonID},
		{"contradiction", "reason", "contradictions", "reason", "'fr'", byPersonID},
	}
	var all []TranslatableItem
	for _, sp := range specs {
		if len(want) > 0 && !want[sp.entityType] {
			continue
		}
		items, err := s.collectTranslatable(ctx, sp.entityType, sp.field, sp.table, sp.textCol, sp.srcLangExpr, sp.personSQL)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

// byPersonID is the personSQL of a table with a person_id column.
const byPersonID = `EXISTS (SELECT 1 FROM persons p WHERE p.id = e.person_id AND ` + CandidateSQL + `)`

// collectTranslatable runs one source query. table/textCol/srcLangExpr/personSQL
// are hardcoded constants from ListTranslatableItems (never user input), so
// the fmt.Sprintf interpolation is safe.
func (s *PostgresStorer) collectTranslatable(ctx context.Context, entityType, field, table, textCol, srcLangExpr, personSQL string) ([]TranslatableItem, error) {
	q := fmt.Sprintf(`
		SELECT e.id, e.%[1]s, %[2]s,
		       coalesce((SELECT jsonb_object_agg(t.lang, t.source_hash)
		                 FROM translations t
		                 WHERE t.entity_type = $1 AND t.entity_id = e.id AND t.field = $2), '{}')::text
		FROM %[3]s e
		WHERE coalesce(e.%[1]s, '') <> '' AND %[4]s`,
		textCol, srcLangExpr, table, personSQL)

	rows, err := s.pool.Query(ctx, q, entityType, field)
	if err != nil {
		return nil, fmt.Errorf("query translatable %s/%s: %w", entityType, field, err)
	}
	defer rows.Close()

	var out []TranslatableItem
	for rows.Next() {
		var id int64
		var text, srcLang, existingJSON string
		if err := rows.Scan(&id, &text, &srcLang, &existingJSON); err != nil {
			return nil, fmt.Errorf("scan translatable %s/%s: %w", entityType, field, err)
		}
		existing := map[string]string{}
		if existingJSON != "" {
			if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
				return nil, fmt.Errorf("unmarshal existing translations: %w", err)
			}
		}
		out = append(out, TranslatableItem{
			EntityType: entityType,
			EntityID:   id,
			Field:      field,
			SourceText: text,
			SrcLang:    srcLang,
			Existing:   existing,
		})
	}
	return out, rows.Err()
}
