package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The audit step pins KEEP verdicts via last_audit_hash: SetContradictionAuditHash
// stores it and ListContradictions returns it, so unchanged rows are skipped on
// the next audit instead of being re-judged.
func TestContradictionAuditHashRoundTrip(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('audit hash person', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'test')`,
			id, fmt.Sprintf("http://ex/%d", id), text)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1 ORDER BY id DESC LIMIT 1`,
			id, personID)
		require.NoError(t, err)
		return id
	}

	fa := insertFact("A: promised to keep the age at 62")
	fb := insertFact("B: abandoned the repeal of the 64 reform")

	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "kept pair", "")
	require.NoError(t, err)

	// Fresh row: no pinned hash.
	rows, err := storer.ListContradictions(ctx, &personID, false, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].LastAuditHash, "fresh contradiction must not have a pinned verdict")

	require.NoError(t, storer.SetContradictionAuditHash(ctx, cid, "deadbeef"))

	rows, err = storer.ListContradictions(ctx, &personID, false, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].LastAuditHash)
	assert.Equal(t, "deadbeef", *rows[0].LastAuditHash)

	// Unknown id errors instead of silently no-oping.
	assert.Error(t, storer.SetContradictionAuditHash(ctx, cid+999999, "x"))
}
