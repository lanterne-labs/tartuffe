package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
)

// FailedDocumentsByError counts the failed documents whose last error
// contains match, per source type. The error is matched case-insensitively.
func (s *PostgresStorer) FailedDocumentsByError(ctx context.Context, match string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source_type, count(*)
		FROM source_documents
		WHERE state = $1 AND last_error ILIKE '%' || $2 || '%'
		GROUP BY 1 ORDER BY 1`, core.DocFailed, match)
	if err != nil {
		return nil, fmt.Errorf("counting failed documents: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("scanning failed documents: %w", err)
		}
		out[st] = n
	}
	return out, rows.Err()
}

// RetryFailedDocuments gives their attempts back to the failed documents
// whose last error contains match, so the next extraction run takes them
// again (ListUnfinishedDocuments). The content stays; the error is kept until
// the next attempt replaces it. It returns the number of documents reset.
//
// For a failure that was not the document's: an LLM outage taken for an
// error of each document (2026-11-09: 18 000 documents marked failed while
// the API's credit was gone).
func (s *PostgresStorer) RetryFailedDocuments(ctx context.Context, match string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE source_documents
		SET attempts = 0
		WHERE state = $1 AND last_error ILIKE '%' || $2 || '%'`, core.DocFailed, match)
	if err != nil {
		return 0, fmt.Errorf("resetting failed documents: %w", err)
	}
	return tag.RowsAffected(), nil
}
