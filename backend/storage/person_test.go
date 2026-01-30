package storage

import (
	"context"
	"testing"
)

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
