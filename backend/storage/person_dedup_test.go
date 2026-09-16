package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertStanceWithFact creates a stance document for personID with the given
// label and attaches factID as a membership. Returns the stance id.
func insertStanceWithFact(ctx context.Context, t *testing.T, s *PostgresStorer, personID, factID int64, label string) int64 {
	t.Helper()
	stanceID, err := s.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     label,
		ClusterEmbedding: mockEmbedding(),
		Summary:          "test stance",
		KeyFactIDs:       []int64{factID},
		FactCount:        1,
	})
	require.NoError(t, err, "insert stance")
	_, err = s.pool.Exec(ctx,
		`INSERT INTO stance_document_facts (stance_document_id, fact_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, stanceID, factID)
	require.NoError(t, err, "insert stance membership")
	return stanceID
}

// TestMergePersons exercises the full FK repoint + collision handling of
// MergePersons: a "from" person with a fact, mention, stance, contradiction,
// twitter handle, and search-queue entry is merged into an "into" person, and a
// colliding stance (same cluster_label) is folded so both facts' memberships
// land on the surviving stance.
func TestMergePersons(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// Survivor "into".
	intoID, err := storer.FindOrInsertPersonWithType(ctx, "alice into", "human")
	require.NoError(t, err)
	// Source "from".
	fromID, err := storer.FindOrInsertPersonWithType(ctx, "alice from", "human")
	require.NoError(t, err)
	require.NotEqual(t, intoID, fromID)

	// Each person gets a fact (-> fact_source + fact_mention + stance via stub).
	intoFactID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity:      "alice into",
		SubjectEntityType:  "human",
		Lang:               "en",
		NormalizedFactText: "alice into supports policy X",
		EmbeddingText:      "supports policy X",
		Embedding:          mockEmbedding(),
		CanonicalVerb:      "SUPPORTED",
		SourceURL:          "http://example.com/into",
		SourceType:         "web",
	})
	require.NoError(t, err)
	fromFactID, fromSourceID, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity:      "alice from",
		SubjectEntityType:  "human",
		Lang:               "en",
		NormalizedFactText: "alice from opposes policy Y",
		EmbeddingText:      "opposes policy Y",
		Embedding:          mockEmbedding(),
		CanonicalVerb:      "OPPOSED",
		SourceURL:          "http://example.com/from",
		SourceType:         "web",
	})
	require.NoError(t, err)
	_ = fromSourceID

	// Contradiction on "from" (fact_a_id < fact_b_id required by CHECK).
	a, b := intoFactID, fromFactID
	if a > b {
		a, b = b, a
	}
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, 'test')`,
		fromID, a, b)
	require.NoError(t, err, "insert contradiction on from")

	// Search-queue entry on "from".
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO search_queue (person_id) VALUES ($1)`, fromID)
	require.NoError(t, err, "insert search_queue on from")

	// Image only on "from"; survivor "into" has none -> merge must backfill it.
	_, err = storer.pool.Exec(ctx,
		`UPDATE persons SET image_url=$2 WHERE id=$1`, fromID, "http://img/alicefrom.jpg")
	require.NoError(t, err, "set image_url on from")

	// Colliding stance: give BOTH persons a stance with the same cluster_label,
	// each attached to its own fact. After merge the survivor's stance must hold
	// both fact memberships, and the duplicate stance must be gone. Fresh facts
	// (no stub stance from insertFactForTest) so each fact has exactly one
	// membership per person — the one-stance-per-(person, fact) invariant.
	insertBareFact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text) VALUES ($1) RETURNING id`, text).Scan(&id))
		return id
	}
	const collideLabel = "shared topic Z"
	collideFactInto := insertBareFact("collide fact of into")
	collideFactFrom := insertBareFact("collide fact of from")
	intoCollideStance := insertStanceWithFact(ctx, t, storer, intoID, collideFactInto, collideLabel)
	fromCollideStance := insertStanceWithFact(ctx, t, storer, fromID, collideFactFrom, collideLabel)
	require.NotEqual(t, intoCollideStance, fromCollideStance)

	// Cross-label duplicate membership: the SAME fact sits in a stance of each
	// person under DIFFERENT labels ("retraites" vs "réforme des retraites").
	// The from stance does not collide, so step c repoints it wholesale to the
	// survivor — who would then hold the fact in two stances. The post-merge
	// dedup (step e2) must collapse that to one membership (the oldest).
	var sharedFactID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO facts (fact_text) VALUES ('shared cross-label fact') RETURNING id`).Scan(&sharedFactID))
	intoSharedStance := insertStanceWithFact(ctx, t, storer, intoID, sharedFactID, "retraites")
	fromSharedStance := insertStanceWithFact(ctx, t, storer, fromID, sharedFactID, "réforme des retraites")

	// --- Merge ---
	require.NoError(t, storer.MergePersons(ctx, fromID, intoID))

	// from row gone.
	var fromExists bool
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM persons WHERE id=$1)`, fromID).Scan(&fromExists))
	assert.False(t, fromExists, "from person should be deleted")

	// into row present.
	var intoExists bool
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM persons WHERE id=$1)`, intoID).Scan(&intoExists))
	assert.True(t, intoExists, "into person should survive")

	// Survivor inherited the dup's image since it had none.
	var intoImage *string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT image_url FROM persons WHERE id=$1`, intoID).Scan(&intoImage))
	require.NotNil(t, intoImage, "survivor image_url should be backfilled from dup")
	assert.Equal(t, "http://img/alicefrom.jpg", *intoImage, "survivor should inherit dup's image_url when it had none")

	// No fact_mentions still point at from; the from fact's mention now points at into.
	var mentionsForFrom int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM fact_mentions WHERE person_id=$1`, fromID).Scan(&mentionsForFrom))
	assert.Equal(t, 0, mentionsForFrom, "no mentions should reference from")

	// Both facts are mentioned by into (no orphaned facts).
	var fromFactMentionedByInto bool
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM fact_mentions fm
			JOIN fact_sources fs ON fs.id=fm.fact_source_id
			WHERE fm.person_id=$1 AND fs.fact_id=$2)`, intoID, fromFactID).Scan(&fromFactMentionedByInto))
	assert.True(t, fromFactMentionedByInto, "from's fact should now be mentioned by into")

	// Search-queue: from had one, into had none -> repointed (into now has one, from none).
	var queueForFrom, queueForInto int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM search_queue WHERE person_id=$1`, fromID).Scan(&queueForFrom))
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM search_queue WHERE person_id=$1`, intoID).Scan(&queueForInto))
	assert.Equal(t, 0, queueForFrom, "no search_queue should reference from")
	assert.Equal(t, 1, queueForInto, "search_queue should be repointed to into")

	// Contradiction repointed.
	var contradictionsForFrom int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM contradictions WHERE person_id=$1`, fromID).Scan(&contradictionsForFrom))
	assert.Equal(t, 0, contradictionsForFrom, "no contradiction should reference from")

	// Collision: the duplicate (from) stance is gone, the surviving (into) stance holds BOTH facts.
	var dupStanceExists bool
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stance_documents WHERE id=$1)`, fromCollideStance).Scan(&dupStanceExists))
	assert.False(t, dupStanceExists, "duplicate (from) stance should be deleted")

	var survivorMemberships int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT count(*) FROM stance_document_facts WHERE stance_document_id=$1`, intoCollideStance).Scan(&survivorMemberships))
	assert.Equal(t, 2, survivorMemberships, "surviving stance should hold both facts' memberships")

	// survivor's collided stance fact_count refreshed to 2.
	var survivorFactCount int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT fact_count FROM stance_documents WHERE id=$1`, intoCollideStance).Scan(&survivorFactCount))
	assert.Equal(t, 2, survivorFactCount, "surviving stance fact_count should be refreshed")

	// No stance_documents reference from.
	var stancesForFrom int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM stance_documents WHERE person_id=$1`, fromID).Scan(&stancesForFrom))
	assert.Equal(t, 0, stancesForFrom, "no stance should reference from")

	// Cross-label duplicate collapsed: the shared fact must be a member of
	// exactly ONE of the survivor's stances (one-stance-per-(person, fact)),
	// and the kept membership is the oldest — the survivor's original stance.
	var sharedMemberships int
	require.NoError(t, storer.pool.QueryRow(ctx, `
		SELECT count(*) FROM stance_document_facts sdf
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE sd.person_id=$1 AND sdf.fact_id=$2`, intoID, sharedFactID).Scan(&sharedMemberships))
	assert.Equal(t, 1, sharedMemberships, "shared fact must keep exactly one membership on the survivor")

	var keptStance int64
	require.NoError(t, storer.pool.QueryRow(ctx, `
		SELECT sdf.stance_document_id FROM stance_document_facts sdf
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE sd.person_id=$1 AND sdf.fact_id=$2`, intoID, sharedFactID).Scan(&keptStance))
	assert.Equal(t, intoSharedStance, keptStance, "oldest membership (survivor's own) must be the one kept")

	// The repointed from-stance survives (non-colliding label) with a refreshed
	// fact_count of 0 after losing its only membership to the dedup.
	var fromSharedCount int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT fact_count FROM stance_documents WHERE id=$1`, fromSharedStance).Scan(&fromSharedCount))
	assert.Equal(t, 0, fromSharedCount, "deduped stance fact_count refreshed to reflect removed membership")
}

// TestMergePersons_SameID guards the self-merge error.
func TestMergePersons_SameID(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	err := storer.MergePersons(ctx, 5, 5)
	assert.Error(t, err, "merging a person into itself should error")
}

func TestChooseCanonical(t *testing.T) {
	t.Run("one curated wins regardless of fact_count", func(t *testing.T) {
		members := []core.PersonDedupMember{
			{ID: 1, Name: "junk dup", FactCount: 100, Curated: false},
			{ID: 2, Name: "real entity", FactCount: 3, Curated: true},
		}
		got, ok := ChooseCanonical(members)
		require.True(t, ok)
		assert.Equal(t, int64(2), got.ID, "curated member must be canonical even with fewer facts")
	})

	t.Run("two curated members skip the cluster", func(t *testing.T) {
		members := []core.PersonDedupMember{
			{ID: 1, Name: "real a", FactCount: 5, Curated: true},
			{ID: 2, Name: "real b", FactCount: 9, Curated: true},
		}
		_, ok := ChooseCanonical(members)
		assert.False(t, ok, "two curated members must skip (never merge real entities)")
	})

	t.Run("no curated: highest fact_count, tie-break lowest id", func(t *testing.T) {
		members := []core.PersonDedupMember{
			{ID: 5, Name: "a", FactCount: 2, Curated: false},
			{ID: 3, Name: "b", FactCount: 7, Curated: false},
			{ID: 9, Name: "c", FactCount: 7, Curated: false},
		}
		got, ok := ChooseCanonical(members)
		require.True(t, ok)
		assert.Equal(t, int64(3), got.ID, "highest fact_count wins, tie-break lowest id")
	})
}
