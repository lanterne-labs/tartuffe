package storage

import (
	"context"
	"strings"
	"testing"

	"digital-mirror/backend/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPersonEntryPointsShareOneLookup covers the single lookup behind every
// person entry point: accent, case and typographic-punctuation variants of one
// name resolve to the one row the first call inserted, whichever entry point
// asks, and GetPersonIDByName finds names whose fold only Postgres unaccent
// knows (ł→l), which the column-only lookup it replaced missed.
func TestPersonEntryPointsShareOneLookup(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	id, inserted, err := storer.AddPerson(ctx, "Éric D’Testperson")
	require.NoError(t, err)
	require.True(t, inserted)

	id2, inserted2, err := storer.AddPersonWithType(ctx, "eric d'testperson", "party")
	require.NoError(t, err)
	assert.False(t, inserted2)
	assert.Equal(t, id, id2)

	id3, err := storer.FindOrInsertPersonWithType(ctx, "ERIC D'TESTPERSON", "human")
	require.NoError(t, err)
	assert.Equal(t, id, id3)

	ids, err := storer.FindOrInsertPersons(ctx, []string{"éric d'testperson", "Eric  D’Testperson"})
	require.NoError(t, err)
	assert.Equal(t, []int64{id}, ids, "variants dedupe to one name before lookup")

	id4, err := storer.GetPersonIDByName(ctx, "Éric D’Testperson")
	require.NoError(t, err)
	assert.Equal(t, id, id4)

	persons, err := storer.GetAllPersons(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 1)
	assert.Equal(t, "eric d'testperson", persons[0].Name)
	assert.Equal(t, "human", persons[0].EntityType, "an existing row keeps its entity_type")

	// ł has no combining mark, so the Go fold keeps it; only the SQL fold
	// maps it to l. Both directions must resolve to the same row.
	idL, inserted, err := storer.AddPerson(ctx, "Łukasz Testperson")
	require.NoError(t, err)
	require.True(t, inserted)
	got, err := storer.GetPersonIDByName(ctx, "Lukasz Testperson")
	require.NoError(t, err)
	assert.Equal(t, idL, got)
	got, inserted, err = storer.AddPerson(ctx, "lukasz testperson")
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, idL, got)

	// A word cut or joined in the source resolves by the name key to the
	// person with the most facts.
	idF, inserted, err := storer.AddPerson(ctx, "Olivier Testfaure")
	require.NoError(t, err)
	require.True(t, inserted)
	for _, variant := range []string{"olivier test-faure", "olivier testfa\u00adure", "oliviertestfaure_"} {
		got, inserted, err = storer.AddPerson(ctx, variant)
		require.NoError(t, err)
		assert.False(t, inserted, variant)
		assert.Equal(t, idF, got, variant)
	}

	_, err = storer.GetPersonIDByName(ctx, "Nobody Testperson")
	require.Error(t, err)
}

// TestFindOrInsertPersons_BatchFoldAndRejection covers the batch entry point:
// two names that only the SQL fold unifies collapse to one row inside the
// transaction, a gate-rejected name is dropped from the result, an all-rejected
// batch returns the rejection, and degenerate inputs are handled.
func TestFindOrInsertPersons_BatchFoldAndRejection(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	ids, err := storer.FindOrInsertPersons(ctx, []string{"Łukasz Testperson", "Lukasz Testperson", "Anne Testperson"})
	require.NoError(t, err)
	require.Len(t, ids, 3)
	assert.Equal(t, ids[0], ids[1], "the second name folds to the row inserted by the first")
	assert.NotEqual(t, ids[0], ids[2])
	anneID := ids[2]

	persons, err := storer.GetAllPersons(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 2)

	storer.SetPersonGate(func(_ context.Context, name, entityType string) error {
		if strings.HasPrefix(name, "junk") {
			return &core.EntityRejectedError{Name: name, EntityType: entityType, Filter: "test", Reason: "junk"}
		}
		return nil
	})
	defer storer.SetPersonGate(nil)

	ids, err = storer.FindOrInsertPersons(ctx, []string{"Junk One", "Anne Testperson"})
	require.NoError(t, err)
	assert.Equal(t, []int64{anneID}, ids, "rejected name is dropped, existing name is not re-gated")

	_, err = storer.FindOrInsertPersons(ctx, []string{"Junk One", "Junk Two"})
	require.True(t, core.IsEntityRejected(err), "all rejected and nothing pre-existing: %v", err)

	_, err = storer.FindOrInsertPersons(ctx, []string{"   "})
	require.Error(t, err, "no valid name after normalization")

	ids, err = storer.FindOrInsertPersons(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, ids)

	persons, err = storer.GetAllPersons(ctx)
	require.NoError(t, err)
	assert.Len(t, persons, 2, "no junk row was inserted")
}

func TestAddPersonWithType(t *testing.T) {
	ctx := context.Background()
	storer, cleanup := setupTestDB(t)
	defer cleanup()

	tests := []struct {
		name       string
		personName string
		entityType string
		wantErr    bool
	}{
		{
			name:       "Create human",
			personName: "Test Person",
			entityType: "human",
			wantErr:    false,
		},
		{
			name:       "Create party",
			personName: "Test Party",
			entityType: "party",
			wantErr:    false,
		},
		{
			name:       "Invalid entity type",
			personName: "Invalid Type Person",
			entityType: "invalid",
			wantErr:    true,
		},
		{
			name:       "Empty name",
			personName: "",
			entityType: "human",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, inserted, err := storer.AddPersonWithType(ctx, tt.personName, tt.entityType)

			if tt.wantErr {
				if err == nil {
					t.Errorf("AddPersonWithType() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("AddPersonWithType() error = %v", err)
				return
			}

			if id == 0 {
				t.Errorf("AddPersonWithType() returned zero ID")
			}

			if !inserted {
				t.Logf("Person already existed (ID: %d)", id)
			} else {
				t.Logf("Created new person (ID: %d)", id)
			}

			// Verify the person was created with correct entity_type
			persons, err := storer.GetAllPersons(ctx)
			if err != nil {
				t.Errorf("GetAllPersons() error = %v", err)
				return
			}

			found := false
			for _, p := range persons {
				if p.ID == id {
					found = true
					if p.EntityType != tt.entityType {
						t.Errorf("Expected entity_type %q, got %q", tt.entityType, p.EntityType)
					}
					break
				}
			}

			if !found {
				t.Errorf("Person with ID %d not found in database", id)
			}
		})
	}
}

func TestAddPersonWithType_Duplicate(t *testing.T) {
	ctx := context.Background()
	storer, cleanup := setupTestDB(t)
	defer cleanup()

	personName := "Duplicate Person"
	entityType := "human"

	// First insert
	id1, inserted1, err := storer.AddPersonWithType(ctx, personName, entityType)
	if err != nil {
		t.Fatalf("First AddPersonWithType() error = %v", err)
	}

	if !inserted1 {
		t.Errorf("First insert should have inserted = true")
	}

	// Second insert (should be idempotent)
	id2, inserted2, err := storer.AddPersonWithType(ctx, personName, entityType)
	if err != nil {
		t.Fatalf("Second AddPersonWithType() error = %v", err)
	}

	if inserted2 {
		t.Errorf("Second insert should have inserted = false")
	}

	if id1 != id2 {
		t.Errorf("Expected same ID for duplicate person, got %d and %d", id1, id2)
	}
}
