package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"digital-mirror/backend/core"
)

// The shared document table references persons.id like a plugin table:
// documents follow a merged person, and deleting a person's facts forgets
// the person's documents so its sources are fetched and extracted again.
func init() {
	RegisterPersonTable(PersonTable{
		Name:       "source_documents",
		RepointSQL: `UPDATE source_documents SET person_id = $2 WHERE person_id = $1`,
		ResetSQL:   `DELETE FROM source_documents WHERE person_id = $1`,
	})
}

// SaveFetchedDocument upserts a fetched document by (source_type, url) into
// the fetched state. A refetch of a known URL replaces the content, stamps
// fetched_at and restarts the attempts: the previous extraction was of other
// content.
func (s *PostgresStorer) SaveFetchedDocument(ctx context.Context, doc core.SourceDocument) (int64, error) {
	meta := []byte("{}")
	if doc.Metadata != nil {
		var err error
		if meta, err = json.Marshal(doc.Metadata); err != nil {
			return 0, fmt.Errorf("encoding document metadata: %w", err)
		}
	}
	var personID any
	if doc.PersonID != 0 {
		personID = doc.PersonID
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO source_documents (source_type, url, person_id, title, content, published_at, metadata, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (source_type, url) DO UPDATE SET
		    person_id = COALESCE(EXCLUDED.person_id, source_documents.person_id),
		    title = EXCLUDED.title,
		    content = EXCLUDED.content,
		    published_at = COALESCE(EXCLUDED.published_at, source_documents.published_at),
		    metadata = EXCLUDED.metadata,
		    fetched_at = NOW(),
		    state = EXCLUDED.state,
		    attempts = 0,
		    extracted_at = NULL,
		    facts_count = 0,
		    last_error = NULL
		RETURNING id`,
		doc.SourceType, doc.URL, personID, doc.Title, doc.Content, doc.PublishedAt, meta, core.DocFetched).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("saving %s document %s: %w", doc.SourceType, doc.URL, err)
	}
	return id, nil
}

// KnownDocumentURLs returns which of urls already have a document of the
// source type, in any state.
func (s *PostgresStorer) KnownDocumentURLs(ctx context.Context, sourceType string, urls []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT url FROM source_documents WHERE source_type = $1 AND url = ANY($2)`, sourceType, urls)
	if err != nil {
		return nil, fmt.Errorf("listing known %s documents: %w", sourceType, err)
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scanning known document: %w", err)
		}
		known[u] = true
	}
	return known, rows.Err()
}

// ListUnfinishedDocuments returns the documents still to extract: fetched
// ones, and failed ones with attempts left, oldest fetch first. personID 0
// means every person.
func (s *PostgresStorer) ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, limit int) ([]core.SourceDocument, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_type, url, COALESCE(person_id, 0), title, content, published_at, metadata,
		       fetched_at, state, attempts, facts_count
		FROM source_documents
		WHERE source_type = $1 AND ($2 = 0 OR person_id = $2)
		  AND (state = $3 OR (state = $4 AND attempts < $5))
		ORDER BY fetched_at, id
		LIMIT $6`,
		sourceType, personID, core.DocFetched, core.DocFailed, core.MaxExtractionAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("listing unfinished %s documents: %w", sourceType, err)
	}
	defer rows.Close()
	var out []core.SourceDocument
	for rows.Next() {
		var d core.SourceDocument
		var meta []byte
		var published *time.Time
		if err := rows.Scan(&d.ID, &d.SourceType, &d.URL, &d.PersonID, &d.Title, &d.Content, &published, &meta,
			&d.FetchedAt, &d.State, &d.Attempts, &d.FactsCount); err != nil {
			return nil, fmt.Errorf("scanning document: %w", err)
		}
		d.PublishedAt = published
		if len(meta) > 0 {
			if err := json.Unmarshal(meta, &d.Metadata); err != nil {
				return nil, fmt.Errorf("decoding metadata of document %d: %w", d.ID, err)
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDocumentExtracted moves the document to extracted with its facts count
// and deletes the raw content: the row is now only the marker that the URL
// was processed.
func (s *PostgresStorer) MarkDocumentExtracted(ctx context.Context, id int64, factsCount int) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE source_documents
		SET state = $2, facts_count = $3, content = '', last_error = NULL,
		    attempts = attempts + 1, extracted_at = NOW()
		WHERE id = $1`, id, core.DocExtracted, factsCount)
	if err != nil {
		return fmt.Errorf("marking document %d extracted: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("marking document %d extracted: no such document", id)
	}
	return nil
}

// MarkDocumentFailed records a failed extraction attempt; the content stays
// for the retry.
func (s *PostgresStorer) MarkDocumentFailed(ctx context.Context, id int64, extractErr error) error {
	msg := ""
	if extractErr != nil {
		msg = extractErr.Error()
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE source_documents
		SET state = $2, last_error = $3, attempts = attempts + 1, extracted_at = NOW()
		WHERE id = $1`, id, core.DocFailed, msg)
	if err != nil {
		return fmt.Errorf("marking document %d failed: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("marking document %d failed: no such document", id)
	}
	return nil
}
