package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PersonTable declares a plugin-owned table that references persons.id, so the
// generic person operations (merge, fact deletion) maintain it without storage
// knowing the plugin. The owning plugin registers it at package init.
type PersonTable struct {
	Name string
	// RepointSQL moves rows from person $1 to person $2 during a merge. It must
	// not violate the table's unique constraints: rows that would collide are
	// left in place for CleanupSQL.
	RepointSQL string
	// CleanupSQL deletes the rows still referencing person $1 after the
	// repoint (optional; needed when RepointSQL can leave collisions behind).
	CleanupSQL string
	// ResetSQL clears the per-person processing state for person $1 when the
	// person's facts are deleted, so the plugin re-processes them (optional).
	ResetSQL string
}

var personTables []PersonTable

// RegisterPersonTable adds a plugin-owned person table to the registry.
func RegisterPersonTable(t PersonTable) {
	if t.Name == "" || t.RepointSQL == "" {
		panic("storage.RegisterPersonTable: Name and RepointSQL are required")
	}
	personTables = append(personTables, t)
}

// repointPersonTables runs every registered table's repoint, then cleanup.
func repointPersonTables(ctx context.Context, tx pgx.Tx, fromID, intoID int64) error {
	for _, t := range personTables {
		if _, err := tx.Exec(ctx, t.RepointSQL, fromID, intoID); err != nil {
			return fmt.Errorf("failed to repoint %s: %w", t.Name, err)
		}
	}
	for _, t := range personTables {
		if t.CleanupSQL == "" {
			continue
		}
		if _, err := tx.Exec(ctx, t.CleanupSQL, fromID); err != nil {
			return fmt.Errorf("failed to clean up leftover %s: %w", t.Name, err)
		}
	}
	return nil
}

// resetPersonTables runs every registered table's reset for personID.
func resetPersonTables(ctx context.Context, tx pgx.Tx, personID int64) error {
	for _, t := range personTables {
		if t.ResetSQL == "" {
			continue
		}
		if _, err := tx.Exec(ctx, t.ResetSQL, personID); err != nil {
			return fmt.Errorf("failed to reset %s: %w", t.Name, err)
		}
	}
	return nil
}
