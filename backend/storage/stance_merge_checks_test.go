package storage

import (
	"context"
	"testing"

	"digital-mirror/backend/core"

	"github.com/stretchr/testify/require"
)

// The merge-sweep memo: a person is listed until the sweep is recorded for its
// current stance set; any stance change (a refreshed summary here) or a new
// rulebook key lists it again; single-stance persons are never listed.
func TestMergeSweepMemo(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_merge_checks, stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	twoStances, _, err := storer.AddPerson(ctx, "merge memo two")
	require.NoError(t, err)
	oneStance, _, err := storer.AddPerson(ctx, "merge memo one")
	require.NoError(t, err)
	// The merge sweep lists the candidates only (candidates.go).
	require.NoError(t, storer.SetCandidate(ctx, twoStances, true))
	require.NoError(t, storer.SetCandidate(ctx, oneStance, true))
	require.NoError(t, err)
	stance := func(personID int64, label string) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: personID, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label,
			KeyFactIDs: []int64{}, FactCount: 0,
		})
		require.NoError(t, err)
		return id
	}
	stance(twoStances, "retraites")
	second := stance(twoStances, "immigration")
	stance(oneStance, "logement")

	names := func(rulebook string) []string {
		persons, err := storer.ListPersonsNeedingMergeSweep(ctx, rulebook)
		require.NoError(t, err)
		out := make([]string, 0, len(persons))
		for _, p := range persons {
			out = append(out, p.Name)
		}
		return out
	}

	require.Equal(t, []string{"merge memo two"}, names("rb1"), "never swept: listed; single-stance person never listed")

	require.NoError(t, storer.RecordMergeSweep(ctx, twoStances, "rb1"))
	require.Empty(t, names("rb1"), "recorded for the current stance set: skipped")
	require.Equal(t, []string{"merge memo two"}, names("rb2"), "a new rulebook re-lists the person")

	require.NoError(t, storer.UpdateStanceSummary(ctx, second, "s immigration (refreshed)", "immigration"))
	require.Equal(t, []string{"merge memo two"}, names("rb1"), "a changed summary re-lists the person")

	require.NoError(t, storer.RecordMergeSweep(ctx, twoStances, "rb1"))
	require.Empty(t, names("rb1"), "re-recorded after the change: skipped again")

	// Recording a person with no stances stores nothing (fingerprint is NULL).
	require.NoError(t, storer.RecordMergeSweep(ctx, oneStance+1000, "rb1"))
	var n int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM stance_merge_checks").Scan(&n))
	require.Equal(t, 1, n)
}

// The stance-lookup memo: a lookup is returned only under its fingerprint,
// "none" is kept as 0, a new answer replaces the old one, and the memo goes
// with its stance.
func TestStanceLookupMemo(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_lookup_checks, stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	person, _, err := storer.AddPerson(ctx, "lookup memo person")
	require.NoError(t, err)
	stance := func(label string) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: person, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s", KeyFactIDs: []int64{},
		})
		require.NoError(t, err)
		return id
	}
	big, small, lone := stance("politique française sur la guerre en Ukraine"), stance("guerre en Ukraine et diplomatie"), stance("football")

	require.NoError(t, storer.RecordStanceLookup(ctx, small, "fp1", big))
	require.NoError(t, storer.RecordStanceLookup(ctx, lone, "fp1", 0))
	got, err := storer.GetStanceLookups(ctx, map[int64]string{small: "fp1", lone: "fp1"})
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{small: big, lone: 0}, got)

	got, err = storer.GetStanceLookups(ctx, map[int64]string{small: "fp2"})
	require.NoError(t, err)
	require.Empty(t, got, "another fingerprint asks again")

	require.NoError(t, storer.RecordStanceLookup(ctx, small, "fp2", 0))
	got, err = storer.GetStanceLookups(ctx, map[int64]string{small: "fp2"})
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{small: 0}, got)

	_, err = storer.pool.Exec(ctx, "DELETE FROM stance_documents WHERE id = $1", small)
	require.NoError(t, err)
	var n int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM stance_lookup_checks WHERE stance_id = $1", small).Scan(&n))
	require.Zero(t, n, "the memo goes with its stance")
}
