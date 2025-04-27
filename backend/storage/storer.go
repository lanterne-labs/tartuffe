package storage

import (
	"context"
	"digital-mirror/backend" // Import backend package for Fact struct
)

// FactStorer defines the interface for persisting and retrieving facts.
type FactStorer interface {
	// StoreFact saves a Fact and its associated people to the database.
	// It handles finding or creating person entries and linking them via fact_mentions.
	// It should ideally operate within a transaction.
	StoreFact(ctx context.Context, fact backend.Fact) error

	// Add other methods as needed, e.g., GetFactsByPerson, GetFactByID, etc.

	// Close terminates the database connection pool.
	Close() // Method to gracefully close resources
} 