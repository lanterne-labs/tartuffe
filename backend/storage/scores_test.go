package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecomputeParentEntityScores pins the parent rollup a contradiction
// triggers: every current parent entity of the person gets members_score =
// average of its current members' honesty scores, honesty_score = the
// aggregate of that with its own_score, and exactly one person_updated event
// carrying the three values; an expired link is not a parent.
func TestRecomputeParentEntityScores(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "DELETE FROM event_log")
	require.NoError(t, err)

	member, err := storer.FindOrInsertPersonWithType(ctx, "rollup member", "human")
	require.NoError(t, err)
	sibling, err := storer.FindOrInsertPersonWithType(ctx, "rollup sibling", "human")
	require.NoError(t, err)
	party, err := storer.FindOrInsertPersonWithType(ctx, "rollup party", "party")
	require.NoError(t, err)
	group, err := storer.FindOrInsertPersonWithType(ctx, "rollup group", "organization")
	require.NoError(t, err)
	former, err := storer.FindOrInsertPersonWithType(ctx, "rollup former party", "party")
	require.NoError(t, err)

	require.NoError(t, storer.CreateAffiliation(ctx, member, party))
	require.NoError(t, storer.CreateAffiliation(ctx, sibling, party))
	require.NoError(t, storer.CreateAffiliation(ctx, member, group))
	require.NoError(t, storer.CreateAffiliation(ctx, member, former))
	_, err = storer.pool.Exec(ctx,
		`UPDATE person_links SET valid_to = CURRENT_DATE - 1 WHERE src_person_id = $1 AND dst_person_id = $2`, member, former)
	require.NoError(t, err)
	// Distinct own scores so the averages are telling.
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET honesty_score = 0.6, own_score = 0.6 WHERE id = $1`, sibling)
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET own_score = 0.9 WHERE id = $1`, party)
	require.NoError(t, err)

	faID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{PublishedAt: &storageTestDate,
		SubjectEntity: "rollup member", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "member promised to lower taxes", EmbeddingText: "promised to lower taxes",
		Embedding: mockEmbedding(), CanonicalVerb: "PROMISED", SourceURL: "http://example.com/a", SourceType: "web",
	})
	require.NoError(t, err)
	fbID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{PublishedAt: &storageTestDate,
		SubjectEntity: "rollup member", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "member voted to raise taxes", EmbeddingText: "voted to raise taxes",
		Embedding: mockEmbedding(), CanonicalVerb: "VOTED_FOR", SourceURL: "http://example.com/b", SourceType: "web",
	})
	require.NoError(t, err)
	if faID > fbID {
		faID, fbID = fbID, faID
	}
	_, _, err = storer.RecordContradictionImmediate(ctx, member, faID, fbID, "raison", "reason")
	require.NoError(t, err)

	var memberScore float64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT honesty_score FROM persons WHERE id = $1`, member).Scan(&memberScore))
	assert.Less(t, memberScore, 1.0, "the contradiction lowered the member's score")

	type scores struct {
		own       float64
		members   *float64
		aggregate float64
	}
	read := func(id int64) scores {
		var s scores
		require.NoError(t, storer.pool.QueryRow(ctx,
			`SELECT own_score, members_score, honesty_score FROM persons WHERE id = $1`, id).Scan(&s.own, &s.members, &s.aggregate))
		return s
	}
	events := func(id int64) int {
		var n int
		require.NoError(t, storer.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM event_log WHERE topic = 'person_updated' AND entity_id = $1
			   AND payload ? 'members_score' AND payload ? 'own_score' AND payload ? 'honesty_score'`, id).Scan(&n))
		return n
	}

	p := read(party)
	require.NotNil(t, p.members)
	assert.InDelta(t, (memberScore+0.6)/2, *p.members, 1e-9, "party members average both current members")
	assert.InDelta(t, 0.9, p.own, 1e-9, "own_score untouched")
	assert.InDelta(t, core.ComputeAggregateScore(0.9, p.members), p.aggregate, 1e-9)
	assert.Equal(t, 1, events(party))

	g := read(group)
	require.NotNil(t, g.members)
	assert.InDelta(t, memberScore, *g.members, 1e-9, "group has one current member")
	assert.InDelta(t, core.ComputeAggregateScore(g.own, g.members), g.aggregate, 1e-9)
	assert.Equal(t, 1, events(group))

	f := read(former)
	assert.Nil(t, f.members, "expired link: not a parent, never rolled up")
	assert.Equal(t, 0, events(former))
}
