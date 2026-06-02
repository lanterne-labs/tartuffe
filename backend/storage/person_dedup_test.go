package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertStanceWithFact creates a stance document for personID with the given
// label and attaches factID as a membership. Returns the stance id.
func insertStanceWithFact(ctx context.Context, t *testing.T, s *PostgresStorer, personID, factID int64, label string) int64 {
	t.Helper()
	stanceID, err := s.InsertStanceDocument(ctx, StanceDocument{
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

	// Disable the entity validator: these synthetic test names ("alice into")
	// would otherwise be rejected as junk by the LLM filter when OPEN_AI_API
	// points at a live model. We're testing merge, not insertion validation.
	storer.SetEntityValidator(nil)

	// Survivor "into".
	intoID, err := storer.FindOrInsertPersonWithType(ctx, "alice into", "human")
	require.NoError(t, err)
	// Source "from".
	fromID, err := storer.FindOrInsertPersonWithType(ctx, "alice from", "human")
	require.NoError(t, err)
	require.NotEqual(t, intoID, fromID)

	// Each person gets a fact (-> fact_source + fact_mention + stance via stub).
	intoFactID, _, err := insertFactForTest(ctx, t, storer, StoreFactInput{
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
	fromFactID, fromSourceID, err := insertFactForTest(ctx, t, storer, StoreFactInput{
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

	// Twitter handle on "from".
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO twitter_handles (person_id, handle) VALUES ($1, 'alicefrom')`, fromID)
	require.NoError(t, err, "insert twitter handle on from")

	// Search-queue entry on "from".
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO search_queue (person_id) VALUES ($1)`, fromID)
	require.NoError(t, err, "insert search_queue on from")

	// Colliding stance: give BOTH persons a stance with the same cluster_label,
	// each attached to its own fact. After merge the survivor's stance must hold
	// both fact memberships, and the duplicate stance must be gone.
	const collideLabel = "shared topic Z"
	intoCollideStance := insertStanceWithFact(ctx, t, storer, intoID, intoFactID, collideLabel)
	fromCollideStance := insertStanceWithFact(ctx, t, storer, fromID, fromFactID, collideLabel)
	require.NotEqual(t, intoCollideStance, fromCollideStance)

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

	// Twitter handle repointed.
	var handlesForFrom, handlesForInto int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM twitter_handles WHERE person_id=$1`, fromID).Scan(&handlesForFrom))
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM twitter_handles WHERE person_id=$1 AND handle='alicefrom'`, intoID).Scan(&handlesForInto))
	assert.Equal(t, 0, handlesForFrom, "no twitter handle should reference from")
	assert.Equal(t, 1, handlesForInto, "twitter handle should be repointed to into")

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
		members := []DedupMember{
			{ID: 1, Name: "junk dup", FactCount: 100, Curated: false},
			{ID: 2, Name: "real entity", FactCount: 3, Curated: true},
		}
		got, ok := ChooseCanonical(members)
		require.True(t, ok)
		assert.Equal(t, int64(2), got.ID, "curated member must be canonical even with fewer facts")
	})

	t.Run("two curated members skip the cluster", func(t *testing.T) {
		members := []DedupMember{
			{ID: 1, Name: "real a", FactCount: 5, Curated: true},
			{ID: 2, Name: "real b", FactCount: 9, Curated: true},
		}
		_, ok := ChooseCanonical(members)
		assert.False(t, ok, "two curated members must skip (never merge real entities)")
	})

	t.Run("no curated: highest fact_count, tie-break lowest id", func(t *testing.T) {
		members := []DedupMember{
			{ID: 5, Name: "a", FactCount: 2, Curated: false},
			{ID: 3, Name: "b", FactCount: 7, Curated: false},
			{ID: 9, Name: "c", FactCount: 7, Curated: false},
		}
		got, ok := ChooseCanonical(members)
		require.True(t, ok)
		assert.Equal(t, int64(3), got.ID, "highest fact_count wins, tie-break lowest id")
	})
}
