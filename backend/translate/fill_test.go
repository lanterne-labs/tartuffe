package translate

import (
	"context"
	"errors"
	"sort"
	"testing"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type upsertCall struct {
	entity, field, lang, text, hash string
	id                              int64
}

type fakeFillStorer struct {
	items     []storage.TranslatableItem
	listErr   error
	upserts   []upsertCall
	upsertErr map[string]error // lang -> error
	legacy    []upsertCall
}

func (f *fakeFillStorer) ListTranslatableItems(_ context.Context, _ ...string) ([]storage.TranslatableItem, error) {
	return f.items, f.listErr
}

func (f *fakeFillStorer) UpsertTranslation(_ context.Context, entityType string, entityID int64, field, lang, text, sourceHash string) error {
	if err := f.upsertErr[lang]; err != nil {
		return err
	}
	f.upserts = append(f.upserts, upsertCall{entityType, field, lang, text, sourceHash, entityID})
	return nil
}

func (f *fakeFillStorer) UpdateLegacyEnColumn(_ context.Context, entityType, field string, entityID int64, text string) error {
	f.legacy = append(f.legacy, upsertCall{entity: entityType, field: field, text: text, id: entityID})
	return nil
}

type echoTranslator struct{}

func (echoTranslator) Translate(_ context.Context, text, _, dst string) (string, error) {
	return dst + ":" + text, nil
}

func langsOf(calls []upsertCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.lang)
	}
	sort.Strings(out)
	return out
}

func TestFill_StaleAndMissingOnly(t *testing.T) {
	fresh := SourceHash("bonjour")
	fs := &fakeFillStorer{items: []storage.TranslatableItem{
		// French fact: en missing, fr is the source language -> only en filled.
		{EntityType: "fact", EntityID: 1, Field: "text", SourceText: "bonjour", SrcLang: "fr", Existing: map[string]string{}},
		// en stored from the CURRENT text -> nothing to do.
		{EntityType: "fact", EntityID: 2, Field: "text", SourceText: "bonjour", SrcLang: "fr", Existing: map[string]string{"en": fresh}},
		// en stored from an OLD text -> stale, refilled.
		{EntityType: "stance", EntityID: 3, Field: "summary", SourceText: "bonjour", SrcLang: "fr", Existing: map[string]string{"en": "stale-hash"}},
		// Unknown source language: both targets filled.
		{EntityType: "contradiction", EntityID: 4, Field: "reason", SourceText: "hola", SrcLang: "auto", Existing: map[string]string{}},
	}}

	stats, err := Fill(context.Background(), fs, echoTranslator{}, FillOptions{})
	require.NoError(t, err)
	assert.Equal(t, FillStats{Filled: 4, Scanned: 4}, stats)

	byID := map[int64][]upsertCall{}
	for _, c := range fs.upserts {
		byID[c.id] = append(byID[c.id], c)
	}
	assert.Equal(t, []string{"en"}, langsOf(byID[1]))
	assert.Empty(t, byID[2], "hash match means done")
	assert.Equal(t, []string{"en"}, langsOf(byID[3]))
	assert.Equal(t, fresh, byID[3][0].hash, "refilled with the current hash")
	assert.Equal(t, []string{"en", "fr"}, langsOf(byID[4]))

	// The legacy _en bridge is written once per EN translation and never for fr.
	require.Len(t, fs.legacy, 3)
	for _, l := range fs.legacy {
		assert.Contains(t, l.text, "en:")
	}
}

func TestFill_LimitAndLangs(t *testing.T) {
	fs := &fakeFillStorer{items: []storage.TranslatableItem{
		{EntityType: "fact", EntityID: 1, Field: "text", SourceText: "a", SrcLang: "fr"},
		{EntityType: "fact", EntityID: 2, Field: "text", SourceText: "b", SrcLang: "fr"},
		{EntityType: "fact", EntityID: 3, Field: "text", SourceText: "c", SrcLang: "fr"},
	}}
	stats, err := Fill(context.Background(), fs, echoTranslator{}, FillOptions{Limit: 2, Langs: []string{"en", "de"}})
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Scanned)
	// Item 1 fills en+de (2), reaching the limit before item 2 is looked at.
	assert.Equal(t, 2, stats.Filled)
	assert.Equal(t, []string{"de", "en"}, langsOf(fs.upserts))
	assert.Len(t, fs.legacy, 1)
}

func TestFill_StoreFailureIsSkipped(t *testing.T) {
	fs := &fakeFillStorer{
		items:     []storage.TranslatableItem{{EntityType: "fact", EntityID: 1, Field: "text", SourceText: "a", SrcLang: "auto"}},
		upsertErr: map[string]error{"en": errors.New("db down")},
	}
	stats, err := Fill(context.Background(), fs, echoTranslator{}, FillOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Filled, "fr stored, en skipped")
	assert.Empty(t, fs.legacy, "no legacy mirror when the EN upsert failed")
}

func TestFill_ListError(t *testing.T) {
	fs := &fakeFillStorer{listErr: errors.New("boom")}
	_, err := Fill(context.Background(), fs, echoTranslator{}, FillOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list translatable items")
}
