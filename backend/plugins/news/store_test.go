package news

import (
	"context"
	"os"
	"testing"

	"digital-mirror/backend/plugins"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDB connects to the test database, opens it without a person
// gate (fixture names are synthetic) and wipes the tables the news
// store reads.
func setupTestDB(t *testing.T) *storage.PostgresStorer {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping news store tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err, "connect to test database")
	t.Cleanup(storer.Close)

	_, err = storer.Pool().Exec(ctx, "TRUNCATE TABLE news_search_log, fact_mentions, fact_sources, facts, persons RESTART IDENTITY CASCADE")
	require.NoError(t, err, "truncate tables")
	return storer
}

// addSource inserts a fact with one source of the given type and a mention
// of personID on that source, straight through SQL so no LLM is involved.
func addSource(ctx context.Context, t *testing.T, storer *storage.PostgresStorer, personID int64, url, sourceType string) {
	t.Helper()
	var factID, sourceID int64
	require.NoError(t, storer.Pool().QueryRow(ctx,
		`INSERT INTO facts (fact_text) VALUES ($1) RETURNING id`, "fact from "+url).Scan(&factID))
	require.NoError(t, storer.Pool().QueryRow(ctx,
		`INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, $3) RETURNING id`,
		factID, url, sourceType).Scan(&sourceID))
	_, err := storer.Pool().Exec(ctx,
		`INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)`, sourceID, personID)
	require.NoError(t, err)
}

// TestShouldSearchNews proves the prioritization rule: a person is searched
// when never searched, skipped once a recent search came back empty, and
// searched again as soon as one recent search produced facts.
func TestShouldSearchNews(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	id, err := storer.FindOrInsertPersonWithType(ctx, "news person", "human")
	require.NoError(t, err)

	should, err := store.ShouldSearchNews(ctx, id, 30)
	require.NoError(t, err)
	assert.True(t, should, "never searched: search")

	require.NoError(t, store.LogNewsSearch(ctx, id, "news person", 0, 0))
	should, err = store.ShouldSearchNews(ctx, id, 30)
	require.NoError(t, err)
	assert.False(t, should, "recent empty search: skip")

	require.NoError(t, store.LogNewsSearch(ctx, id, "news person", 4, 2))
	should, err = store.ShouldSearchNews(ctx, id, 30)
	require.NoError(t, err)
	assert.True(t, should, "a recent search produced facts: search again")
}

// TestNewsURLsForPerson proves the rebuild work list: distinct news URLs
// mentioning the person, sorted, other source types and other persons excluded.
func TestNewsURLsForPerson(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	alice, err := storer.FindOrInsertPersonWithType(ctx, "alice news", "human")
	require.NoError(t, err)
	bob, err := storer.FindOrInsertPersonWithType(ctx, "bob news", "human")
	require.NoError(t, err)

	addSource(ctx, t, storer, alice, "https://example.org/b", "news")
	addSource(ctx, t, storer, alice, "https://example.org/a", "news")
	addSource(ctx, t, storer, alice, "https://example.org/a", "news") // second fact from the same URL
	addSource(ctx, t, storer, alice, "https://example.org/tweet", "twitter")
	addSource(ctx, t, storer, bob, "https://example.org/c", "news")

	urls, err := store.NewsURLsForPerson(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.org/a", "https://example.org/b"}, urls)

	urls, err = store.NewsURLsForPerson(ctx, bob)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.org/c"}, urls)
}

// TestPrioritizeTargets proves ordering and the history filter: parties come
// first, a person whose recent searches were all empty is dropped only when
// SkipNoResults is set, and a target without a person id is always kept.
func TestPrioritizeTargets(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)
	provider := &Provider{store: store}

	emptyID, err := storer.FindOrInsertPersonWithType(ctx, "empty person", "human")
	require.NoError(t, err)
	coveredID, err := storer.FindOrInsertPersonWithType(ctx, "covered person", "human")
	require.NoError(t, err)
	partyID, err := storer.FindOrInsertPersonWithType(ctx, "some party", "party")
	require.NoError(t, err)
	require.NoError(t, store.LogNewsSearch(ctx, emptyID, "empty person", 0, 0))
	require.NoError(t, store.LogNewsSearch(ctx, coveredID, "covered person", 3, 1))

	fresh := plugins.Target{PersonID: 999999, Name: "fresh person", EntityType: "human"}
	empty := plugins.Target{PersonID: emptyID, Name: "empty person", EntityType: "human"}
	party := plugins.Target{PersonID: partyID, Name: "some party", EntityType: "party"}
	covered := plugins.Target{PersonID: coveredID, Name: "covered person", EntityType: "human"}
	adhoc := plugins.Target{Name: "adhoc name", EntityType: "human"}
	input := []plugins.Target{fresh, empty, party, covered, adhoc}

	got, err := provider.PrioritizeTargets(ctx, input, PrioritizationOptions{LookbackDays: 30, SkipNoResults: true, PartiesFirst: true})
	require.NoError(t, err)
	assert.Equal(t, []plugins.Target{party, fresh, covered, adhoc}, got)

	got, err = provider.PrioritizeTargets(ctx, input, PrioritizationOptions{LookbackDays: 30, SkipNoResults: false, PartiesFirst: false})
	require.NoError(t, err)
	assert.Equal(t, []plugins.Target{fresh, empty, covered, adhoc, party}, got)
}
