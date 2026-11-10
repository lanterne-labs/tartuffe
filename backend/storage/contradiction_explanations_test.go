package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContradictionExplanations follows an explanation through its life: a
// candidate's contradiction waits for one, with the two facts as the reader
// sees them (date, source, first quote); once written it no longer waits,
// the feed, the stance card and the share preview give it, and the
// translation phase sees it; a changed fact or a new prompt version makes it
// wait again.
func TestContradictionExplanations(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID, otherID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, attributes) VALUES ('explain person', 'human', '{"candidate": true}') RETURNING id`,
	).Scan(&personID))
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('explain bystander', 'human') RETURNING id`,
	).Scan(&otherID))

	insertFact := func(person int64, text, quote string, at time.Time) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewHalfVector(mockEmbedding()),
		).Scan(&id))
		// The first source has no quote: the explanation takes the first quote there is.
		for i, q := range []string{"", quote} {
			_, err := storer.pool.Exec(ctx,
				`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at) VALUES ($1, $2, $3, 'test', $4)`,
				id, fmt.Sprintf("http://ex%d.test/%d", i, id), q, at.Add(time.Duration(i)*time.Hour))
			require.NoError(t, err)
		}
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1`, id, person)
		require.NoError(t, err)
		return id
	}
	d1, d2 := time.Date(2021, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	fa := insertFact(personID, "said this campaign is the last", "« Sur mon nom ? Je crois, oui. »", d1)
	fb := insertFact(personID, "announced a fourth candidacy", "il a officialisé sa quatrième candidature", d2)
	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "the judge's line", "")
	require.NoError(t, err)
	// The contradiction of a person who is not a candidate is not explained
	// by the pass, only when named.
	oa := insertFact(otherID, "bystander said yes", "oui", d1)
	ob := insertFact(otherID, "bystander said no", "non", d2)
	oid, _, err := storer.RecordContradictionImmediate(ctx, otherID, oa, ob, "other", "")
	require.NoError(t, err)

	mine := func(rows []ContradictionToExplain) []int64 {
		var ids []int64
		for _, r := range rows {
			if r.ID == cid || r.ID == oid {
				ids = append(ids, r.ID)
			}
		}
		return ids
	}

	todo, err := storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{})
	require.NoError(t, err)
	require.Equal(t, []int64{cid}, mine(todo), "the candidate's contradiction waits, the bystander's does not")
	var c ContradictionToExplain
	for _, r := range todo {
		if r.ID == cid {
			c = r
		}
	}
	assert.Equal(t, "explain person", c.Person)
	assert.Equal(t, "the judge's line", c.Reason)
	sides := map[int64]ExplainSide{c.A.FactID: c.A, c.B.FactID: c.B}
	assert.Equal(t, "« Sur mon nom ? Je crois, oui. »", sides[fa].Quote, "the first quote, past the source without one")
	assert.Equal(t, fmt.Sprintf("http://ex0.test/%d", fa), sides[fa].SourceURL, "the first source")
	require.NotNil(t, sides[fa].Date)
	assert.True(t, sides[fa].Date.Equal(d1))
	assert.Equal(t, "announced a fourth candidacy", sides[fb].Text)

	named, err := storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{IDs: []int64{oid}})
	require.NoError(t, err)
	assert.Equal(t, []int64{oid}, mine(named), "a named contradiction is given whatever its person")
	byPerson, err := storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{PersonID: otherID})
	require.NoError(t, err)
	assert.Equal(t, []int64{oid}, mine(byPerson))

	require.NoError(t, storer.SetContradictionExplanation(ctx, cid, "En 2021 il disait que c'était sa dernière campagne.", c.Hash))
	todo, err = storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{})
	require.NoError(t, err)
	assert.Empty(t, mine(todo), "an explained contradiction no longer waits")
	all, err := storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{All: true})
	require.NoError(t, err)
	assert.Equal(t, []int64{cid}, mine(all), "All gives it again")

	// The read paths give the explanation.
	var feedText *string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT explanation FROM contradictions WHERE id = $1`, cid).Scan(&feedText))
	require.NotNil(t, feedText)
	share, err := storer.GetShareTarget(ctx, "contradiction", cid, 0)
	require.NoError(t, err)
	assert.Equal(t, "En 2021 il disait que c'était sa dernière campagne.", share.Quote, "the preview carries the explanation, not the judge's line")
	items, err := storer.ListTranslatableItems(ctx, "contradiction")
	require.NoError(t, err)
	found := false
	for _, it := range items {
		if it.EntityID == cid && it.Field == "explanation" {
			found = true
			assert.Equal(t, "En 2021 il disait que c'était sa dernière campagne.", it.SourceText)
		}
	}
	assert.True(t, found, "the explanation is translated like the reason")
	require.NoError(t, storer.UpdateLegacyEnColumn(ctx, "contradiction", "explanation", cid, "In 2021 he said it was his last campaign."))
	var en string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT explanation_en FROM contradictions WHERE id = $1`, cid).Scan(&en))
	assert.Equal(t, "In 2021 he said it was his last campaign.", en)

	// A new prompt version, then a reworded fact: the explanation waits again.
	todo, err = storer.ListContradictionsToExplain(ctx, "2", ExplainFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int64{cid}, mine(todo), "another prompt version")
	_, err = storer.pool.Exec(ctx, `UPDATE facts SET fact_text = 'said, hedged, this campaign is the last' WHERE id = $1`, fa)
	require.NoError(t, err)
	todo, err = storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int64{cid}, mine(todo), "a reworded fact")

	// A new text clears the English one until it is translated again.
	require.NoError(t, storer.SetContradictionExplanation(ctx, cid, "nouveau texte", todo[0].Hash))
	var enAfter *string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT explanation_en FROM contradictions WHERE id = $1`, cid).Scan(&enAfter))
	assert.Nil(t, enAfter)
	assert.Error(t, storer.SetContradictionExplanation(ctx, -1, "x", "h"), "an unknown contradiction is an error")
}
