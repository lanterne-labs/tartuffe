package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"digital-mirror/backend/core"

	"github.com/stretchr/testify/require"
)

// The document state machine's persistence: save = fetched, known URLs,
// the unfinished listing (fetched, failed under the cap), extracted deletes
// the content, and a refetch restarts everything.
func TestSourceDocumentStateMachine(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "documents person")
	require.NoError(t, err)

	ids, err := storer.SaveFetchedDocuments(ctx, []core.SourceDocument{
		{SourceType: "news", URL: "https://d.test/a", PersonID: pid, Title: "A", Content: "body A",
			Metadata: map[string]any{"source_name": "Le Monde"}},
		{SourceType: "news", URL: "https://d.test/b", PersonID: pid, Content: "body B"},
	})
	require.NoError(t, err)
	require.Len(t, ids, 2, "every saved document is returned by url")
	id, other := ids["https://d.test/a"], ids["https://d.test/b"]
	require.NotZero(t, id)
	require.NotZero(t, other)

	known, err := storer.KnownDocumentURLs(ctx, "news", []string{"https://d.test/a", "https://d.test/zzz"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"https://d.test/a": true}, known)
	unknownType, err := storer.KnownDocumentURLs(ctx, "twitter", []string{"https://d.test/a"})
	require.NoError(t, err)
	require.Empty(t, unknownType, "known is per source type")

	unfinished, err := storer.ListUnfinishedDocuments(ctx, "news", pid, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, unfinished, 2)
	require.Equal(t, "body A", unfinished[0].Content)
	require.Equal(t, "Le Monde", unfinished[0].Metadata["source_name"])
	require.Equal(t, core.DocFetched, unfinished[0].State)

	// A fetched document is a visit: the URL is not visitable inside the interval.
	visitable, _, err := storer.IsURLVisitable(ctx, "https://d.test/a", 24)
	require.NoError(t, err)
	require.False(t, visitable)

	require.NoError(t, storer.MarkDocumentExtracted(ctx, id, 3))
	require.NoError(t, storer.MarkDocumentFailed(ctx, other, errors.New("llm down")))
	unfinished, err = storer.ListUnfinishedDocuments(ctx, "news", pid, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, unfinished, 1, "extracted is finished, failed under the cap is not")
	require.Equal(t, other, unfinished[0].ID)
	require.Equal(t, 1, unfinished[0].Attempts)

	var content string
	var facts int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT content, facts_count FROM source_documents WHERE id = $1", id).Scan(&content, &facts))
	require.Equal(t, "body A", content, "extracted keeps the content")
	require.Equal(t, 3, facts)

	for i := 1; i < core.MaxExtractionAttempts; i++ {
		require.NoError(t, storer.MarkDocumentFailed(ctx, other, errors.New("llm down")))
	}
	unfinished, err = storer.ListUnfinishedDocuments(ctx, "news", pid, time.Time{}, 10)
	require.NoError(t, err)
	require.Empty(t, unfinished, "at the attempt cap the document is left alone")

	// A refetch restarts the machine for that URL.
	again, err := storer.SaveFetchedDocuments(ctx, []core.SourceDocument{{SourceType: "news", URL: "https://d.test/b", PersonID: pid, Content: "body B v2"}})
	require.NoError(t, err)
	require.Equal(t, other, again["https://d.test/b"])
	unfinished, err = storer.ListUnfinishedDocuments(ctx, "news", 0, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	require.Equal(t, "body B v2", unfinished[0].Content)
	require.Equal(t, 0, unfinished[0].Attempts)

	require.Error(t, storer.MarkDocumentExtracted(ctx, id+100000, 0), "marking an unknown document is an error")

	// An empty batch is a no-op, not an error.
	empty, err := storer.SaveFetchedDocuments(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// A publication floor keeps a recent-content feed off the backlog: older and
// undated documents are left to the backfill (mirror-29j: the daily débats feed
// walked 2,000 old transcripts past the run deadline).
// A document stored under a Google redirect link is listed, and moved to
// the page's own URL unless that page already has its document.
func TestMoveDocumentURLFromGoogleRedirect(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "redirect person")
	require.NoError(t, err)
	ids, err := storer.SaveFetchedDocuments(ctx, []core.SourceDocument{
		{SourceType: "news", URL: "https://www.google.com/goto?url=AAA", PersonID: pid, Content: "body A"},
		{SourceType: "news", URL: "https://www.google.com/goto?url=BBB", PersonID: pid, Content: "body B"},
		{SourceType: "news", URL: "https://d.test/b", PersonID: pid, Content: "body B, the page itself"},
		{SourceType: "news", URL: "https://d.test/c", PersonID: pid, Content: "a page"},
	})
	require.NoError(t, err)

	under, err := storer.ListDocumentsUnderGoogleRedirect(ctx, "news")
	require.NoError(t, err)
	require.Equal(t, []DocumentURL{
		{ID: ids["https://www.google.com/goto?url=AAA"], URL: "https://www.google.com/goto?url=AAA"},
		{ID: ids["https://www.google.com/goto?url=BBB"], URL: "https://www.google.com/goto?url=BBB"},
	}, under)

	moved, err := storer.MoveDocumentURL(ctx, under[0].ID, "https://d.test/a")
	require.NoError(t, err)
	require.True(t, moved)
	moved, err = storer.MoveDocumentURL(ctx, under[1].ID, "https://d.test/b")
	require.NoError(t, err)
	require.False(t, moved, "the page is already known under its own URL")

	known, err := storer.KnownDocumentURLs(ctx, "news", []string{"https://d.test/a", "https://www.google.com/goto?url=AAA", "https://www.google.com/goto?url=BBB"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"https://d.test/a": true, "https://www.google.com/goto?url=BBB": true}, known)
}

func TestListUnfinishedDocumentsPublishedSince(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	now := time.Now().UTC()
	old, recent := now.AddDate(0, 0, -30), now.AddDate(0, 0, -1)
	ids, err := storer.SaveFetchedDocuments(ctx, []core.SourceDocument{
		{SourceType: "assemblee_debat", URL: "https://d.test/old", Content: "old", PublishedAt: &old},
		{SourceType: "assemblee_debat", URL: "https://d.test/recent", Content: "recent", PublishedAt: &recent},
		{SourceType: "assemblee_debat", URL: "https://d.test/undated", Content: "undated"},
	})
	require.NoError(t, err)

	all, err := storer.ListUnfinishedDocuments(ctx, "assemblee_debat", 0, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, all, 3, "no floor: every unfinished document")

	windowed, err := storer.ListUnfinishedDocuments(ctx, "assemblee_debat", 0, now.AddDate(0, 0, -4), 10)
	require.NoError(t, err)
	require.Len(t, windowed, 1)
	require.Equal(t, ids["https://d.test/recent"], windowed[0].ID)
}
