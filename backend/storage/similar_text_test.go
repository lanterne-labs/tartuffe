package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/core"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// FindSimilarActiveFacts unions two candidate sources: embedding distance and
// trigram similarity of the fact text. A near-verbatim restatement whose
// embedding is far away is still a candidate (with its real distance), a
// distinct fact with a low text score and a far embedding is not, and
// duplicate-marked facts and other persons' facts stay excluded.
func TestFindSimilarActiveFactsUnionsTextMatches(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "similar text person")
	require.NoError(t, err)
	other, _, err := storer.AddPerson(ctx, "similar text other")
	require.NoError(t, err)

	seq := 0
	insert := func(person int64, text string, emb []float32) int64 {
		seq++
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(emb)).Scan(&id))
		var sid int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, 'news') RETURNING id",
			id, "https://t.test/"+string(rune('a'+seq))).Scan(&sid))
		_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person)
		require.NoError(t, err)
		return id
	}
	const claim = "Olivier Faure a assuré qu'il serait aux côtés des Gilets jaunes s'ils se soulèvent de nouveau."
	query := makeUnitEmbedding(0)
	nearEmb := insert(pid, "Olivier Faure a affirmé que la priorité est le pouvoir d'achat.", makeMixedEmbedding(0, 1, 0.95, 0.05))
	farText := insert(pid, "Olivier Faure a affirmé qu'il serait aux côtés des Gilets jaunes s'ils se soulevaient de nouveau en 2026.", makeUnitEmbedding(9))
	distinct := insert(pid, "Olivier Faure a proposé une taxe sur les super-héritages pour financer les retraites.", makeUnitEmbedding(8))
	dupText := insert(pid, claim, makeUnitEmbedding(7))
	foreign := insert(other, claim, makeUnitEmbedding(6))
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", farText, dupText)
	require.NoError(t, err)

	got, err := storer.FindSimilarActiveFacts(ctx, core.SimilarFactQuery{PersonIDs: []int64{pid}, Embedding: query, MaxDistance: 0.3, Text: claim, TextFloor: 0.40, Limit: 10})
	require.NoError(t, err)
	ids := make([]int64, 0, len(got))
	for _, sf := range got {
		ids = append(ids, sf.ID)
	}
	require.Equal(t, []int64{nearEmb, farText}, ids, "embedding hit first, then the text match; distinct, duplicate-marked and foreign facts excluded")
	require.Greater(t, got[1].Distance, 0.3, "a text-only candidate keeps its real embedding distance")
	_ = distinct
	_ = foreign

	// Without a text, only the embedding source applies.
	got, err = storer.FindSimilarActiveFacts(ctx, core.SimilarFactQuery{PersonIDs: []int64{pid}, Embedding: query, MaxDistance: 0.3, TextFloor: 0.40, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, nearEmb, got[0].ID)
}

// insertQuotedFact stores an active fact of person with one source of
// sourceType carrying quote, published on day ("" = no date); it returns the
// fact id.
func insertQuotedFact(ctx context.Context, t *testing.T, storer *PostgresStorer, person int64, text, quote, sourceType, day string, emb []float32) int64 {
	t.Helper()
	var id int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(emb)).Scan(&id))
	var sid int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		"INSERT INTO fact_sources (fact_id, source_url, source_type, original_quote, published_at) VALUES ($1, $2, $3, $4, NULLIF($5, '')::timestamptz) RETURNING id",
		id, fmt.Sprintf("https://www.q.test/%d", id), sourceType, quote, day).Scan(&sid))
	_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person)
	require.NoError(t, err)
	return id
}

const (
	quoteGiletsJaunes  = "Je serai aux côtés des Gilets jaunes s'ils se soulèvent de nouveau, je le dis clairement."
	quoteGiletsJaunes2 = "« Je serai aux côtés des Gilets jaunes s'ils se soulèvent de nouveau », a-t-il dit."
	quoteRetraites     = "Il faut une taxe sur les super-héritages pour financer nos retraites."
	quoteEcole         = "L'école doit rester le premier budget de la nation."
)

var testEvidenceRule = core.GroupEvidence{SourceTypes: []string{"news", "twitter"}, QuoteFloor: 0.30, MaxDateGapDays: 7}

// With the evidence rule, a candidate needs an own source of an allowed type
// whose quote overlaps the new fact's quote or whose date is at most 7 days
// from the new fact's date. A fact with another quote weeks away, a fact with
// no date and another quote, and a structured source are left out.
func TestFindSimilarActiveFactsAppliesEvidenceRule(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "evidence rule person")
	require.NoError(t, err)
	emb := makeUnitEmbedding(0)
	sameQuoteOld := insertQuotedFact(ctx, t, storer, pid, "Il sera aux côtés des Gilets jaunes.", quoteGiletsJaunes2, "news", "2026-03-01", emb)
	otherQuoteSameWeek := insertQuotedFact(ctx, t, storer, pid, "Il défend la taxe sur les héritages.", quoteRetraites, "news", "2026-09-08", emb)
	insertQuotedFact(ctx, t, storer, pid, "Il défend l'école.", quoteEcole, "news", "2026-08-01", emb)
	insertQuotedFact(ctx, t, storer, pid, "Il défend l'école, encore.", quoteEcole, "news", "", emb)
	insertQuotedFact(ctx, t, storer, pid, "Il a voté pour le texte.", quoteGiletsJaunes2, "assemblee_law_page", "2026-09-10", emb)

	day := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	q := core.SimilarFactQuery{
		PersonIDs: []int64{pid}, Embedding: emb, MaxDistance: 0.3, Limit: 10,
		Evidence: &testEvidenceRule, Quote: quoteGiletsJaunes, PublishedAt: &day,
	}
	got, err := storer.FindSimilarActiveFacts(ctx, q)
	require.NoError(t, err)
	ids := []int64{}
	for _, sf := range got {
		ids = append(ids, sf.ID)
	}
	require.ElementsMatch(t, []int64{sameQuoteOld, otherQuoteSameWeek}, ids, "quote overlap at any date, or any quote within 7 days")

	// A new fact with no date keeps only the quote overlap.
	q.PublishedAt = nil
	got, err = storer.FindSimilarActiveFacts(ctx, q)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, sameQuoteOld, got[0].ID)

	q.Evidence = &core.GroupEvidence{QuoteFloor: 0.30, MaxDateGapDays: 7}
	_, err = storer.FindSimilarActiveFacts(ctx, q)
	require.Error(t, err, "an evidence rule without source types must fail")
}

// ListGroupEvidencePairs keeps the given pairs that meet the evidence rule,
// as given; LoadClaimSources returns what the gate reads.
func TestListGroupEvidencePairsAndClaimSources(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "evidence pairs person")
	require.NoError(t, err)
	emb := makeUnitEmbedding(0)
	a := insertQuotedFact(ctx, t, storer, pid, "A", quoteGiletsJaunes, "news", "2026-03-01", emb)
	b := insertQuotedFact(ctx, t, storer, pid, "B", quoteGiletsJaunes2, "twitter", "2026-09-10", emb)
	c := insertQuotedFact(ctx, t, storer, pid, "C", quoteRetraites, "news", "2026-09-12", emb)
	d := insertQuotedFact(ctx, t, storer, pid, "D", quoteEcole, "news", "", emb)
	vote := insertQuotedFact(ctx, t, storer, pid, "V", quoteGiletsJaunes, "assemblee_law_page", "2026-03-01", emb)

	got, err := storer.ListGroupEvidencePairs(ctx, [][2]int64{{b, a}, {b, c}, {a, c}, {c, d}, {a, vote}}, testEvidenceRule)
	require.NoError(t, err)
	require.Equal(t, [][2]int64{{b, a}, {b, c}}, got,
		"quote overlap months apart, and 2 days apart with another quote; not another quote months apart, a missing date, or a structured source")

	got, err = storer.ListGroupEvidencePairs(ctx, nil, testEvidenceRule)
	require.NoError(t, err)
	require.Empty(t, got)

	sources, err := storer.LoadClaimSources(ctx, []int64{b, d, vote}, testEvidenceRule.SourceTypes)
	require.NoError(t, err)
	require.Len(t, sources, 2, "the structured source is not evidence")
	require.Len(t, sources[b], 1)
	require.Equal(t, "q.test", sources[b][0].Outlet)
	require.Equal(t, quoteGiletsJaunes2, sources[b][0].Quote)
	require.Equal(t, "2026-09-10", sources[b][0].PublishedAt.UTC().Format("2006-01-02"))
	require.Nil(t, sources[d][0].PublishedAt)
}

// ListTextSimilarFactPairs surfaces the pairs, among the given ids only, whose
// texts pass the trigram floor, each once as [smaller, larger].
func TestListTextSimilarFactPairs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	insert := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(makeUnitEmbedding(0))).Scan(&id))
		return id
	}
	a := insert("Olivier Faure a assuré qu'il serait aux côtés des Gilets jaunes s'ils se soulèvent de nouveau.")
	b := insert("Olivier Faure a affirmé qu'il serait aux côtés des Gilets jaunes s'ils se soulevaient de nouveau en 2026.")
	c := insert("Olivier Faure a proposé une taxe sur les super-héritages pour financer les retraites.")
	outside := insert("Olivier Faure a assuré qu'il serait aux côtés des Gilets jaunes s'ils se soulèvent.")

	pairs, err := storer.ListTextSimilarFactPairs(ctx, []int64{b, a, c}, 0.40)
	require.NoError(t, err)
	require.Equal(t, [][2]int64{{a, b}}, pairs, "only the restatement pair, ordered [smaller, larger]; ids outside the set are ignored")
	_ = outside
}

// GetFoldFactsForPerson returns the person's active facts with embeddings,
// whoever else a fact mentions, leaving out facts already folded and facts
// the person is not mentioned on.
func TestGetFoldFactsForPersonExcludesFolded(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "fold facts person")
	require.NoError(t, err)
	other, _, err := storer.AddPerson(ctx, "fold facts other")
	require.NoError(t, err)
	seq := 0
	insert := func(text string, persons ...int64) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(makeUnitEmbedding(0))).Scan(&id))
		for _, person := range persons {
			seq++
			var sid int64
			require.NoError(t, storer.pool.QueryRow(ctx,
				"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, 'news') RETURNING id",
				id, fmt.Sprintf("https://f.test/%d", seq)).Scan(&sid))
			_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person)
			require.NoError(t, err)
		}
		return id
	}
	own := insert("own fact", pid)
	alsoOther := insert("fact also mentioning another person", pid, other)
	folded := insert("folded member", pid)
	require.NoError(t, storer.MergeFactIntoGroup(ctx, folded, own))
	insert("someone else's fact", other)

	got, err := storer.GetFoldFactsForPerson(ctx, pid)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, own, got[0].ID)
	require.Equal(t, alsoOther, got[1].ID)
	require.Len(t, got[0].Embedding, len(makeUnitEmbedding(0)))
}
