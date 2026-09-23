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

	// Bridge: UpdateLegacyEnColumn mirrors EN back into the legacy fact_text_en column.
	require.NoError(t, storer.UpdateLegacyEnColumn(ctx, "fact", "text", fid, "bridged EN value"))
	var en *string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT fact_text_en FROM facts WHERE id=$1`, fid).Scan(&en))
	require.NotNil(t, en)
	assert.Equal(t, "bridged EN value", *en)
}

// TestTranslations_FollowDeletes: deleting a fact removes its translations and
// only its own — not another fact's, nor another entity type's with the same id.
func TestTranslations_FollowDeletes(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	insert := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, lang, embedding) VALUES ($1, 'fr', $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		return id
	}
	gone, kept := insert("xtrans: fait supprimé"), insert("xtrans: fait gardé")
	require.NoError(t, storer.UpsertTranslation(ctx, "fact", gone, "text", "en", "deleted fact", "h"))
	require.NoError(t, storer.UpsertTranslation(ctx, "fact", kept, "text", "en", "kept fact", "h"))
	require.NoError(t, storer.UpsertTranslation(ctx, "stance", gone, "label", "en", "a stance sharing the id", "h"))

	_, err := storer.pool.Exec(ctx, `DELETE FROM facts WHERE id = $1`, gone)
	require.NoError(t, err)

	count := func(entityType string, id int64) int {
		var n int
		require.NoError(t, storer.pool.QueryRow(ctx,
			`SELECT count(*) FROM translations WHERE entity_type = $1 AND entity_id = $2`, entityType, id).Scan(&n))
		return n
	}
	assert.Equal(t, 0, count("fact", gone), "the deleted fact's translation goes with it")
	assert.Equal(t, 1, count("fact", kept))
	assert.Equal(t, 1, count("stance", gone), "another entity type with the same id is untouched")
	_, err = storer.pool.Exec(ctx, `DELETE FROM translations WHERE entity_type = 'stance' AND entity_id = $1`, gone)
	require.NoError(t, err)
}
