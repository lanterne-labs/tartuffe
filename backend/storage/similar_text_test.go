package storage

import (
	"context"
	"fmt"
	"testing"

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
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(emb)).Scan(&id))
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

	got, err := storer.FindSimilarActiveFacts(ctx, []int64{pid}, query, 0.3, claim, 0.40, 10)
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
	got, err = storer.FindSimilarActiveFacts(ctx, []int64{pid}, query, 0.3, "", 0.40, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, nearEmb, got[0].ID)
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
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(makeUnitEmbedding(0))).Scan(&id))
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
// leaving out facts that mention another person too (shared co-actor facts
// never fold) and facts already folded.
func TestGetFoldFactsForPersonExcludesSharedAndFolded(t *testing.T) {
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
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(makeUnitEmbedding(0))).Scan(&id))
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
	shared := insert("shared vote fact", pid, other)
	folded := insert("folded member", pid)
	require.NoError(t, storer.MarkFactAsDuplicate(ctx, folded, own))
	insert("someone else's fact", other)

	got, err := storer.GetFoldFactsForPerson(ctx, pid)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, own, got[0].ID)
	require.Len(t, got[0].Embedding, len(makeUnitEmbedding(0)))
	_ = shared
}
