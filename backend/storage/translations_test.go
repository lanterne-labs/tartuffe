package storage

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranslations_UpsertAndList verifies the generic translation store: upsert
// (insert + conflict-update), and that ListTranslatableItems surfaces a fact with
// its source language and already-stored translation hashes.
func TestTranslations_UpsertAndList(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var fid int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO facts (fact_text, lang, embedding) VALUES ($1, 'fr', $2) RETURNING id`,
		"xtrans: un fait de test en français", pgvector.NewVector(mockEmbedding()),
	).Scan(&fid))

	// Insert a translation.
	require.NoError(t, storer.UpsertTranslation(ctx, "fact", fid, "text", "en", "xtrans: a test fact in English", "hash1"))

	// ListTranslatableItems should surface our fact with src lang + existing hash.
	items, err := storer.ListTranslatableItems(ctx)
	require.NoError(t, err)
	var found *TranslatableItem
	for i := range items {
		if items[i].EntityType == "fact" && items[i].EntityID == fid {
			found = &items[i]
			break
		}
	}
	require.NotNil(t, found, "fact should appear in translatable items")
	assert.Equal(t, "xtrans: un fait de test en français", found.SourceText)
	assert.Equal(t, "fr", found.SrcLang)
	assert.Equal(t, "hash1", found.Existing["en"], "existing translation hash should be reported")

	// Upserting the same (entity,field,lang) updates text + hash (conflict path).
	require.NoError(t, storer.UpsertTranslation(ctx, "fact", fid, "text", "en", "xtrans: updated English", "hash2"))
	var txt, h string
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT text, source_hash FROM translations WHERE entity_type='fact' AND entity_id=$1 AND field='text' AND lang='en'`, fid,
	).Scan(&txt, &h))
	assert.Equal(t, "xtrans: updated English", txt)
	assert.Equal(t, "hash2", h)
}
