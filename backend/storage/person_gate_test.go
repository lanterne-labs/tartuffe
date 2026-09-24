package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"digital-mirror/backend/core"
	"digital-mirror/backend/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectAllGate rejects every new candidate and records what it was asked.
func rejectAllGate(asked *[]string) PersonGate {
	return func(_ context.Context, name, entityType string) error {
		*asked = append(*asked, name+"/"+entityType)
		return &core.EntityRejectedError{Name: name, EntityType: entityType, Filter: "test", Reason: "junk"}
	}
}

// TestPersonGate_RejectsNewPersons covers the hook on every new-person path:
// AddPerson, AddPersonWithType and FindOrInsertPersonWithType return the
// gate's *core.EntityRejectedError unchanged, an existing person is never
// re-gated, and a nil gate allows everything.
func TestPersonGate_RejectsNewPersons(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// Existing row inserted while the gate is nil (allow-all).
	existingID, inserted, err := storer.AddPerson(ctx, "Known Person")
	require.NoError(t, err)
	require.True(t, inserted)

	var asked []string
	storer.SetPersonGate(rejectAllGate(&asked))

	_, _, err = storer.AddPerson(ctx, "Junk Person")
	var rej *core.EntityRejectedError
	require.True(t, errors.As(err, &rej), "AddPerson must return the gate's error: %v", err)
	assert.Equal(t, "junk person", rej.Name)
	assert.Equal(t, "test", rej.Filter)

	_, _, err = storer.AddPersonWithType(ctx, "Junk Party", "party")
	require.True(t, errors.As(err, &rej), "AddPersonWithType must return the gate's error: %v", err)
	assert.Equal(t, "party", rej.EntityType)

	_, err = storer.FindOrInsertPersonWithType(ctx, "Junk Org", "organization")
	require.True(t, errors.As(err, &rej), "FindOrInsertPersonWithType must return the gate's error: %v", err)

	// Existing persons bypass the gate.
	id, inserted, err := storer.AddPerson(ctx, "Known Person")
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, existingID, id)
	id, err = storer.FindOrInsertPersonWithType(ctx, "Known Person", "human")
	require.NoError(t, err)
	assert.Equal(t, existingID, id)

	assert.Equal(t, []string{"junk person/", "junk party/party", "junk org/organization"}, asked)

	// Only the known person exists.
	persons, err := storer.GetAllPersons(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 1)

	// nil gate allows again.
	storer.SetPersonGate(nil)
	_, inserted, err = storer.AddPerson(ctx, "Junk Person")
	require.NoError(t, err)
	assert.True(t, inserted)
}

// TestPersonGate_FactInserterSkipsRejectedSubject covers the batch path: a
// fact whose new subject the gate rejects is skipped (not an error) and a fact
// about an existing subject in the same batch is inserted without re-gating.
func TestPersonGate_FactInserterSkipsRejectedSubject(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	_, _, err := storer.AddPerson(ctx, "Real Person")
	require.NoError(t, err)

	var asked []string
	storer.SetPersonGate(rejectAllGate(&asked))

	inserter := core.NewFactInserter(storer, &stubStanceProcessor{storer: storer})
	res, err := inserter.InsertFacts(ctx, []core.StoreFactInput{
		{
			SubjectEntity:      "Junk Subject",
			NormalizedFactText: "junk subject said X",
			Embedding:          mockEmbedding(),
			OriginalQuote:      "X",
			SourceURL:          "http://example.com/junk",
			SourceType:         "test",
			Lang:               "en",
		},
		{
			SubjectEntity:      "Real Person",
			NormalizedFactText: "real person said Y",
			Embedding:          mockEmbedding(),
			OriginalQuote:      "Y",
			SourceURL:          "http://example.com/real",
			SourceType:         "test",
			Lang:               "en",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Skipped, "rejected subject is a per-fact skip")
	assert.Equal(t, 1, res.Inserted)

	persons, err := storer.GetAllPersons(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 1, "no gated candidate was inserted")
	assert.Equal(t, "real person", persons[0].Name)
	assert.Equal(t, []string{"junk subject/"}, asked, "only the new subject reaches the gate; the existing subject is not re-gated")
}

// TestPersonGate_UnavailableValidatorFailsTheFact: a gate that cannot reach
// the LLM is not a rejection. The fact insert fails with the error wrapping
// llm.ErrUnavailable (so the document is retried), nothing is inserted, and
// the fact is not counted as a skipped rejected subject.
func TestPersonGate_UnavailableValidatorFailsTheFact(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	storer.SetPersonGate(func(_ context.Context, name, _ string) error {
		return fmt.Errorf("validating new entity %q: %w", name, llm.ErrUnavailable)
	})
	inserter := core.NewFactInserter(storer, &stubStanceProcessor{storer: storer})
	res, err := inserter.InsertFacts(ctx, []core.StoreFactInput{{
		SubjectEntity:      "New Subject",
		NormalizedFactText: "new subject said X",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "X",
		SourceURL:          "http://example.com/new",
		SourceType:         "test",
		Lang:               "en",
	}})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.False(t, core.IsEntityRejected(err))
	assert.Zero(t, res.Skipped)
	assert.Zero(t, res.Inserted)

	persons, err := storer.GetAllPersons(ctx)
	require.NoError(t, err)
	assert.Empty(t, persons, "no person validated by nobody")
}
