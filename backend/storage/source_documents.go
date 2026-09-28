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

// saveBatch is how many documents one SaveFetchedDocuments round trip
// carries. A débats run saves its whole backlog at once (~9k documents on
// the first run), which is minutes of latency one INSERT at a time.
const saveBatch = 500

// SaveFetchedDocuments upserts documents by (source_type, url) into the
// fetched state and returns their ids keyed by url. A refetch of a known URL
// replaces the content, stamps fetched_at and restarts the attempts: the
// previous extraction was of other content.
func (s *PostgresStorer) SaveFetchedDocuments(ctx context.Context, docs []core.SourceDocument) (map[string]int64, error) {
	ids := make(map[string]int64, len(docs))
	for start := 0; start < len(docs); start += saveBatch {
		end := start + saveBatch
		if end > len(docs) {
			end = len(docs)
		}
		if err := s.saveFetchedChunk(ctx, docs[start:end], ids); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *PostgresStorer) saveFetchedChunk(ctx context.Context, docs []core.SourceDocument, into map[string]int64) error {
	n := len(docs)
	sourceTypes := make([]string, n)
	urls := make([]string, n)
	personIDs := make([]*int64, n)
	titles := make([]string, n)
	contents := make([]string, n)
	published := make([]*time.Time, n)
	metas := make([][]byte, n)
	for i, doc := range docs {
		meta := []byte("{}")
		if doc.Metadata != nil {
			var err error
			if meta, err = json.Marshal(doc.Metadata); err != nil {
				return fmt.Errorf("encoding metadata of %s: %w", doc.URL, err)
			}
		}
		sourceTypes[i], urls[i], titles[i], contents[i], published[i], metas[i] =
			doc.SourceType, doc.URL, doc.Title, doc.Content, doc.PublishedAt, meta
		if doc.PersonID != 0 {
			id := doc.PersonID
			personIDs[i] = &id
		}
	}

	rows, err := s.pool.Query(ctx, `
		INSERT INTO source_documents (source_type, url, person_id, title, content, published_at, metadata, state)
		SELECT d.source_type, d.url, d.person_id, d.title, d.content, d.published_at, d.metadata, $8
		FROM unnest($1::text[], $2::text[], $3::bigint[], $4::text[], $5::text[], $6::timestamptz[], $7::jsonb[])
		     AS d(source_type, url, person_id, title, content, published_at, metadata)
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
		RETURNING id, url`,
		sourceTypes, urls, personIDs, titles, contents, published, metas, core.DocFetched)
	if err != nil {
		return fmt.Errorf("saving %d documents: %w", n, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return fmt.Errorf("scanning saved document: %w", err)
		}
		into[url] = id
	}
	return rows.Err()
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
// means every person; a zero publishedSince any publication date.
func (s *PostgresStorer) ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, publishedSince time.Time, limit int) ([]core.SourceDocument, error) {
	var since *time.Time
	if !publishedSince.IsZero() {
		since = &publishedSince
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_type, url, COALESCE(person_id, 0), title, content, published_at, metadata,
		       fetched_at, state, attempts, facts_count
		FROM source_documents
		WHERE source_type = $1 AND ($2 = 0 OR person_id = $2)
		  AND (state = $3 OR (state = $4 AND attempts < $5))
		  AND ($7::timestamptz IS NULL OR published_at >= $7)
		ORDER BY fetched_at, id
		LIMIT $6`,
		sourceType, personID, core.DocFetched, core.DocFailed, core.MaxExtractionAttempts, limit, since)
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
