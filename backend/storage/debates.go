package storage

import (
	"context"
	"fmt"
	"time"
)

// DebateDocumentKey identifies one (séance, speaker) transcript document by
// the compte-rendu uid and the speaker's AN actor id — the identity that is
// stable across republished séances and threshold changes.
type DebateDocumentKey struct {
	CRUID   string
	ActorID string
}

// DebateDocumentRecord is one processed transcript document.
type DebateDocumentRecord struct {
	DebateDocumentKey
	SourceURL  string
	PersonID   int64
	SeanceDate time.Time
	Paragraphs int
	Chars      int
	FactsCount int
}

// FilterUnprocessedDebateDocuments returns, in input order, the keys that have
// no debate_documents row — the documents the débats step still has to
// extract. One round-trip whatever the archive size.
func (s *PostgresStorer) FilterUnprocessedDebateDocuments(ctx context.Context, keys []DebateDocumentKey) ([]DebateDocumentKey, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	uids := make([]string, len(keys))
	actors := make([]string, len(keys))
	for i, k := range keys {
		uids[i], actors[i] = k.CRUID, k.ActorID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.cr_uid, d.actor_id
		FROM debate_documents d
		JOIN unnest($1::text[], $2::text[]) AS k(cr_uid, actor_id)
		  ON k.cr_uid = d.cr_uid AND k.actor_id = d.actor_id`, uids, actors)
	if err != nil {
		return nil, fmt.Errorf("querying processed debate documents: %w", err)
	}
	defer rows.Close()
	seen := make(map[DebateDocumentKey]bool, len(keys))
	for rows.Next() {
		var k DebateDocumentKey
		if err := rows.Scan(&k.CRUID, &k.ActorID); err != nil {
			return nil, fmt.Errorf("scanning processed debate document: %w", err)
		}
		seen[k] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating processed debate documents: %w", err)
	}
	out := make([]DebateDocumentKey, 0, len(keys))
	for _, k := range keys {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// RecordDebateDocument marks a transcript document as processed (upsert).
func (s *PostgresStorer) RecordDebateDocument(ctx context.Context, rec DebateDocumentRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO debate_documents (cr_uid, actor_id, source_url, person_id, seance_date, paragraphs, chars, facts_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (cr_uid, actor_id) DO UPDATE
		SET source_url = EXCLUDED.source_url, person_id = EXCLUDED.person_id,
		    paragraphs = EXCLUDED.paragraphs, chars = EXCLUDED.chars,
		    facts_count = EXCLUDED.facts_count, processed_at = NOW()`,
		rec.CRUID, rec.ActorID, rec.SourceURL, rec.PersonID, rec.SeanceDate, rec.Paragraphs, rec.Chars, rec.FactsCount)
	if err != nil {
		return fmt.Errorf("recording debate document %s/%s: %w", rec.CRUID, rec.ActorID, err)
	}
	return nil
}

// GetPersonsByAssembleeID returns every person carrying an Assemblée actor id
// (attributes.assemblee_id, "PA..."), keyed by that id. The débats ingestion
// joins transcript paragraphs to persons on this id — no name matching. When
// two rows carry the same id (un-merged duplicates), the one with more facts
// wins, then the lowest id, so the choice is deterministic.
func (s *PostgresStorer) GetPersonsByAssembleeID(ctx context.Context) (map[string]PersonInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (attributes->>'assemblee_id')
		       id, name, COALESCE(entity_type, 'human'), attributes->>'assemblee_id'
		FROM persons
		WHERE attributes ? 'assemblee_id'
		ORDER BY attributes->>'assemblee_id', fact_count DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying persons by assemblee_id: %w", err)
	}
	defer rows.Close()
	out := map[string]PersonInfo{}
	for rows.Next() {
		var p PersonInfo
		var aid string
		if err := rows.Scan(&p.ID, &p.Name, &p.EntityType, &aid); err != nil {
			return nil, fmt.Errorf("scanning person by assemblee_id: %w", err)
		}
		if aid != "" {
			out[aid] = p
		}
	}
	return out, rows.Err()
}
