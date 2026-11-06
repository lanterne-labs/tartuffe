package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPickBotContradictions: a candidate's contradiction is picked, the one
// reviewed real first; a reported, reviewed false or drafted one is left
// out, a skipped draft comes back; a non-candidate is never picked.
func TestPickBotContradictions(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)
	_, err := storer.pool.Exec(ctx, `TRUNCATE x_posts, x_accounts, contradiction_reviews, twitter_handles RESTART IDENTITY CASCADE`)
	require.NoError(t, err)

	pick := func() []int64 {
		t.Helper()
		got, err := storer.PickBotContradictions(ctx, 5, nil)
		require.NoError(t, err)
		var ids []int64
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		return ids
	}
	assert.Empty(t, pick(), "not a candidate")
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET attributes = jsonb_build_object('candidate', 'true') WHERE id = $1`, f.person)
	require.NoError(t, err)
	assert.Equal(t, []int64{f.contradiction}, pick())

	second, _, err := storer.RecordContradictionImmediate(ctx, f.person, f.facts[1], f.facts[2], "65 ans puis la réforme.", "")
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `INSERT INTO contradiction_reviews (contradiction_id, person_id, person_name, fact_a_id, fact_b_id, fact_a_text, fact_b_text, reason, detected_at, verdict, reviewer)
		VALUES ($1, $2, 'report person', $3, $4, 'a', 'b', 'r', now(), 'real', 'test')`, second, f.person, f.facts[1], f.facts[2])
	require.NoError(t, err)
	// One per person, the reviewed one first.
	assert.Equal(t, []int64{second}, pick())
	c, err := storer.GetBotContradiction(ctx, second)
	require.NoError(t, err)
	assert.True(t, c.Reviewed)
	assert.Equal(t, f.facts[1], c.A.ID, "the earlier fact first")

	// A draft holds it; a skipped one lets it come back.
	var post int64
	require.NoError(t, storer.pool.QueryRow(ctx, `INSERT INTO x_posts (contradiction_id, person_id, slot) VALUES ($1, $2, now()) RETURNING id`, second, f.person).Scan(&post))
	assert.Equal(t, []int64{f.contradiction}, pick())
	_, err = storer.pool.Exec(ctx, `UPDATE x_posts SET status = 'skipped' WHERE id = $1`, post)
	require.NoError(t, err)
	assert.Equal(t, []int64{second}, pick())

	// A report on one of its facts, or a false review, leaves it out.
	_, err = storer.pool.Exec(ctx, `INSERT INTO user_reports (user_id, target_kind, target_id, person_id, person_name, target_text, reason)
		VALUES ($1, 'fact', $2, $3, 'report person', 't', 'ce fait est faux, voyez la source')`, f.user, f.facts[2], f.person)
	require.NoError(t, err)
	assert.Equal(t, []int64{f.contradiction}, pick())
	_, err = storer.pool.Exec(ctx, `INSERT INTO contradiction_reviews (contradiction_id, person_id, person_name, fact_a_id, fact_b_id, fact_a_text, fact_b_text, reason, detected_at, verdict, reviewer)
		VALUES ($1, $2, 'report person', $3, $4, 'a', 'b', 'r', now(), 'false', 'test')`, f.contradiction, f.person, f.facts[0], f.facts[1])
	require.NoError(t, err)
	assert.Empty(t, pick())
}

// TestBotTagPool: the person's handles (a refused one left out), their
// party's approved account, the outlets of the sources' sites, the others.
func TestBotTagPool(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)
	_, err := storer.pool.Exec(ctx, `TRUNCATE x_posts, x_accounts, twitter_handles RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `INSERT INTO twitter_handles (person_id, handle) VALUES ($1, 'ReportPerson'), ($1, 'oldaccount')`, f.person)
	require.NoError(t, err)
	var party int64
	require.NoError(t, storer.pool.QueryRow(ctx, `INSERT INTO persons (name, entity_type) VALUES ('le parti', 'party') RETURNING id`).Scan(&party))
	_, err = storer.pool.Exec(ctx, `INSERT INTO person_links (src_person_id, dst_person_id, link_type) VALUES ($1, $2, 'affiliation')`, f.person, party)
	require.NoError(t, err)

	add := func(a XAccount) {
		t.Helper()
		_, err := storer.UpsertXAccount(ctx, a)
		require.NoError(t, err)
	}
	ex := "www.ex"
	other := "other.fr"
	add(XAccount{Handle: "@OldAccount", PersonID: &f.person, Status: "refused"})
	add(XAccount{Handle: "leparti", Category: "party", PersonID: &party, Status: "approved"})
	add(XAccount{Handle: "exnews", Category: "outlet", Domain: &ex, Status: "approved"})
	add(XAccount{Handle: "othernews", Category: "outlet", Domain: &other, Status: "approved"})
	add(XAccount{Handle: "factcheck", Category: "factcheck", Status: "approved"})
	add(XAccount{Handle: "maybe", Category: "association"})

	c, err := storer.GetBotContradiction(ctx, f.contradiction)
	require.NoError(t, err)
	pool, err := storer.BotTagPool(ctx, c, []string{"ex"})
	require.NoError(t, err)
	assert.Equal(t, []string{"reportperson"}, pool.Person)
	require.Len(t, pool.Parties, 1)
	assert.Equal(t, "leparti", pool.Parties[0].Handle)
	require.Len(t, pool.Outlets, 1)
	assert.Equal(t, "exnews", pool.Outlets[0].Handle)
	var others []string
	for _, a := range pool.Others {
		others = append(others, a.Handle)
	}
	assert.ElementsMatch(t, []string{"exnews", "othernews", "factcheck"}, others, "approved only, no party")

	// An update keeps the fields not given.
	updated, err := storer.UpsertXAccount(ctx, XAccount{Handle: "exnews", Note: "quotidien"})
	require.NoError(t, err)
	assert.Equal(t, "approved", updated.Status)
	assert.Equal(t, "outlet", updated.Category)
	require.NotNil(t, updated.Domain)
	assert.Equal(t, "ex", *updated.Domain)
	n, err := storer.SetXAccountStatus(ctx, "refused", []string{"@ExNews", "nobody"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}
