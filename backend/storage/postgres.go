package storage

import (
	"context"
	// "digital-mirror/backend" // Removed unused import
	"digital-mirror/backend/embedding" // Assuming backend.Fact is no longer needed here directly
	"digital-mirror/backend/util"      // Import the new util package
	"fmt"
	"os"
	// "strings" // No longer needed directly
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/rs/zerolog/log" // Import zerolog
)

// --- Structs for Storing ---

// StoreFactInput encapsulates the data needed to store a fact and its source.
// This replaces the direct use of backend.Fact in the storage layer.
type StoreFactInput struct {
	// NormalizedFactText is the core, normalized factual statement.
	NormalizedFactText string
	// Embedding is the vector embedding of the NormalizedFactText.
	Embedding []float32
	// OriginalQuote is the exact text snippet from the source.
	OriginalQuote string
	// Topics are relevant keywords or categories.
	Topics []string
	// InvolvedPeople lists the names of individuals mentioned.
	InvolvedPeople []string
	// SourceURL is the URL where the statement was found.
	SourceURL string
	// PublishedAt is the publication date of the source content, if available.
	PublishedAt *time.Time
}

// PostgresStorer implements the storage logic using PostgreSQL.
type PostgresStorer struct {
	pool     *pgxpool.Pool
	embedder embedding.EmbeddingGenerator // Keep embedder if StoreFact generates embeddings, remove if pre-generated
}

// NewPostgresStorerFromEnv creates a new PostgresStorer by connecting to the database
// specified by the DATABASE_URL environment variable and using the provided embedder.
func NewPostgresStorerFromEnv(ctx context.Context) (*PostgresStorer, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		log.Debug().Msg("Registering pgvector types with pgx connection")
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool with pgvector: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Debug().Msg("Successfully connected to database with pgvector type registered.")
	// Note: Removed embedder initialization here, assuming it's passed in StoreFactInput
	return &PostgresStorer{pool: pool}, nil
}

// Close closes the underlying database connection pool.
func (s *PostgresStorer) Close() {
	if s.pool != nil {
		log.Debug().Msg("Closing database connection pool.")
		s.pool.Close()
	}
}

// Constants for Similarity Search
const (
	// embeddingSimilarityThreshold determines how close embeddings must be (cosine distance)
	// Lower value means higher similarity (0 = identical, 2 = opposite).
	// Adjust based on testing with your embedding model.
	embeddingSimilarityThreshold = 0.15 // Example: requires fairly high similarity
	// textSimilarityThreshold could be used with pg_trgm or other text similarity checks if needed.
	// For now, we rely primarily on embedding + exact match on text for simplicity.
	// textSimilarityThreshold = 0.8
	// maxCandidatesToCheck limits how many embedding matches we check text against.
	maxCandidatesToCheck = 5
)

// StoreFact saves a normalized fact, links its source, and connects involved people.
// It handles duplicate facts based on embedding similarity and normalized text.
func (s *PostgresStorer) StoreFact(ctx context.Context, input StoreFactInput) (factID int64, factSourceID int64, err error) {
	// Validate input
	if input.NormalizedFactText == "" || len(input.Embedding) == 0 || input.SourceURL == "" || len(input.InvolvedPeople) == 0 {
		return 0, 0, fmt.Errorf("invalid input: missing required fields (NormalizedFactText, Embedding, SourceURL, InvolvedPeople)")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during transaction: %v", r)
			log.Error().Interface("panic_info", r).Msg("Panic recovered during StoreFact transaction") // Log panic
		}
		if err != nil {
			// Log error before rollback attempt
			log.Error().Err(err).Msg("Rolling back transaction due to error")
			if rbErr := tx.Rollback(ctx); rbErr != nil {
				log.Error().Err(rbErr).Msg("Failed to rollback transaction")
				err = fmt.Errorf("transaction error: %v; rollback error: %w", err, rbErr) // Combine errors
			}
		}
		// Implicit commit if err is nil
	}()

	// Step A: Find or Insert Fact
	factID, err = s.findOrInsertFact(ctx, tx, input.NormalizedFactText, input.Embedding, input.Topics)
	if err != nil {
		return 0, 0, fmt.Errorf("step A failed (findOrInsertFact): %w", err)
	}

	// Step B: Find or Insert Persons
	personIDs, err := s.findOrInsertPersons(ctx, tx, input.InvolvedPeople, input.NormalizedFactText)
	if err != nil {
		return 0, 0, fmt.Errorf("step B failed (findOrInsertPersons): %w", err)
	}

	// Step C: Link Source to Fact
	factSourceID, err = s.linkSourceToFact(ctx, tx, factID, input.SourceURL, input.OriginalQuote, input.PublishedAt)
	if err != nil {
		return 0, 0, fmt.Errorf("step C failed (linkSourceToFact): %w", err)
	}

	// Step D: Link Persons to Source Mention
	err = s.linkPersonsToSourceMention(ctx, tx, factSourceID, personIDs)
	if err != nil {
		return 0, 0, fmt.Errorf("step D failed (linkPersonsToSourceMention): %w", err)
	}

	// --- Commit ---
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return factID, factSourceID, nil // Success, err is nil
}

// findOrInsertFact searches for an existing fact based on embedding and text,
// or inserts a new one if no match is found. Operates within a transaction.
func (s *PostgresStorer) findOrInsertFact(ctx context.Context, tx pgx.Tx, text string, embedding []float32, topics []string) (factID int64, err error) {
	var existingFactID int64 = -1 // Use -1 to indicate not found

	// 1. Search for similar facts by embedding
	searchStmt := `
		SELECT id, fact_text
		FROM facts
		ORDER BY embedding <-> $1
		LIMIT $2`
	rows, err := tx.Query(ctx, searchStmt, pgvector.NewVector(embedding), maxCandidatesToCheck)
	if err != nil {
		return 0, fmt.Errorf("failed to search for similar facts: %w", err)
	}
	defer rows.Close()

	// 2. Check candidates for text match
	for rows.Next() {
		var candidateID int64
		var candidateText string
		if err = rows.Scan(&candidateID, &candidateText); err != nil {
			return 0, fmt.Errorf("failed to scan similar fact candidate: %w", err)
		}
		// Simple exact match for now
		if candidateText == text {
			existingFactID = candidateID
			// Debug level for found items
			log.Debug().Int64("fact_id", existingFactID).Str("fact_text", text).Msg("Found existing fact matching text")
			break
		}
	}
	if rows.Err() != nil {
		return 0, fmt.Errorf("error iterating through similar fact candidates: %w", rows.Err())
	}

	// 3. Insert if no match found
	if existingFactID == -1 {
		insertFactStmt := `
			INSERT INTO facts (fact_text, embedding, topics)
			VALUES ($1, $2, $3)
			ON CONFLICT (fact_text) DO UPDATE SET embedding = EXCLUDED.embedding, topics = EXCLUDED.topics -- Update embedding/topics if text conflicts
			RETURNING id`
		err = tx.QueryRow(ctx, insertFactStmt,
			text,
			pgvector.NewVector(embedding),
			topics,
		).Scan(&factID)
		if err != nil {
			return 0, fmt.Errorf("failed to insert new fact: %w", err)
		}
		// Info level for new items created
		log.Info().Int64("fact_id", factID).Str("fact_text", text).Msg("Inserted new fact")
	} else {
		factID = existingFactID // Use the found ID
	}

	return factID, nil
}

// findOrInsertPersons finds or inserts persons based on normalized names. Operates within a transaction.
func (s *PostgresStorer) findOrInsertPersons(ctx context.Context, tx pgx.Tx, personNames []string, factTextForLogging string) (personIDs []int64, err error) {
	personIDs = make([]int64, 0, len(personNames))
	personInsertStmt := `
		INSERT INTO persons (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name -- Keep existing ID
		RETURNING id`
	for _, personName := range personNames {
		normalizedName := util.NormalizeString(personName)
		if normalizedName == "" {
			// Warn level for skipped data
			log.Warn().Str("original_name", personName).Str("related_fact_text", factTextForLogging).Msg("Skipping empty person name")
			continue
		}
		var personID int64
		err = tx.QueryRow(ctx, personInsertStmt, normalizedName).Scan(&personID)
		if err != nil {
			return nil, fmt.Errorf("failed to insert/find person '%s' (normalized: '%s'): %w", personName, normalizedName, err)
		}
		personIDs = append(personIDs, personID)
		// Debug: log person ID mapping?
		// log.Debug().Str("normalized_name", normalizedName).Int64("person_id", personID).Msg("Found/inserted person")
	}
	if len(personIDs) == 0 {
		// This case should have been caught by input validation, but double-check.
		return nil, fmt.Errorf("no valid persons associated with fact after normalization: '%s'", factTextForLogging)
	}
	return personIDs, nil
}

// linkSourceToFact inserts or updates a record linking a source URL to a fact. Operates within a transaction.
func (s *PostgresStorer) linkSourceToFact(ctx context.Context, tx pgx.Tx, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time) (factSourceID int64, err error) {
	sourceInsertStmt := `
		INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (fact_id, source_url) DO UPDATE SET
			original_quote = EXCLUDED.original_quote, -- Update quote/date if URL reappears for same fact
			published_at = EXCLUDED.published_at,
			scraped_at = CURRENT_TIMESTAMP
		RETURNING id`
	err = tx.QueryRow(ctx, sourceInsertStmt,
		factID,
		sourceURL,
		originalQuote,
		publishedAt,
	).Scan(&factSourceID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert/update fact source for fact ID %d and URL '%s': %w", factID, sourceURL, err)
	}
	// Info or Debug? Let's use Debug as it happens for every fact.
	log.Debug().Int64("fact_source_id", factSourceID).Str("source_url", sourceURL).Int64("fact_id", factID).Msg("Linked source to fact")
	return factSourceID, nil
}

// linkPersonsToSourceMention links persons to a specific fact source mention. Operates within a transaction.
func (s *PostgresStorer) linkPersonsToSourceMention(ctx context.Context, tx pgx.Tx, factSourceID int64, personIDs []int64) (err error) {
	mentionInsertStmt := `
		INSERT INTO fact_mentions (fact_source_id, person_id)
		VALUES ($1, $2)
		ON CONFLICT (fact_source_id, person_id) DO NOTHING` // Silently ignore duplicates here
	for _, personID := range personIDs {
		_, err = tx.Exec(ctx, mentionInsertStmt, factSourceID, personID)
		if err != nil {
			return fmt.Errorf("failed to insert mention for fact source %d and person %d: %w", factSourceID, personID, err)
		}
		// Debug: log mention link?
		// log.Debug().Int64("fact_source_id", factSourceID).Int64("person_id", personID).Msg("Linked person mention to source")
	}
	return nil // Success
}

// --- Add methods for URL pre-check if desired ---

// CheckSourceURLExists checks if a given URL is already present in the fact_sources table.
func (s *PostgresStorer) CheckSourceURLExists(ctx context.Context, url string) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM fact_sources WHERE source_url = $1)`
	err := s.pool.QueryRow(ctx, query, url).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if source URL exists '%s': %w", url, err)
	}
	return exists, nil
}

// GetAllPersons retrieves a list of all unique person names from the persons table.
func (s *PostgresStorer) GetAllPersons(ctx context.Context) ([]string, error) {
	query := `SELECT name FROM persons ORDER BY name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan person name: %w", err)
		}
		persons = append(persons, name)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over persons rows: %w", rows.Err())
	}

	return persons, nil
}

// AddPerson inserts a new person into the persons table if they don't already exist.
// It uses the normalized version of the name.
func (s *PostgresStorer) AddPerson(ctx context.Context, name string) (id int64, inserted bool, err error) {
	normalizedName := util.NormalizeString(name)
	if normalizedName == "" {
		return 0, false, fmt.Errorf("cannot add empty person name")
	}

	stmt := `
		INSERT INTO persons (name) VALUES ($1)
		ON CONFLICT (name) DO NOTHING
		RETURNING id
	`
	
	// Use QueryRow because RETURNING id might not return a row if the conflict occurs.
	// We need to handle the case where no row is returned.
	err = s.pool.QueryRow(ctx, stmt, normalizedName).Scan(&id)

	if err == pgx.ErrNoRows {
		// Conflict occurred, person already exists. We need to get their ID.
		log.Debug().Str("name", normalizedName).Msg("Person already exists, fetching ID")
		selectStmt := `SELECT id FROM persons WHERE name = $1`
		err = s.pool.QueryRow(ctx, selectStmt, normalizedName).Scan(&id)
		if err != nil {
			return 0, false, fmt.Errorf("failed to fetch existing person ID for '%s': %w", normalizedName, err)
		}
		return id, false, nil // Return existing ID, indicate not inserted
	} else if err != nil {
		// Any other error during insert
		return 0, false, fmt.Errorf("failed to insert person '%s': %w", normalizedName, err)
	}

	// If we reach here, the insert was successful and returned an ID.
	return id, true, nil // Return new ID, indicate inserted
}

// FactDetail includes the normalized fact text and details about one of its sources.
type FactDetail struct {
	FactText      string
	SourceURL     string
	OriginalQuote string
	FactID        int64 // ID of the fact itself
	FactSourceID  int64 // ID of the specific source link
}

// GetAllFactsGroupedByPerson retrieves all facts and their sources, grouped by the persons mentioned.
// Returns a map where keys are person names and values are slices of FactDetail.
func (s *PostgresStorer) GetAllFactsGroupedByPerson(ctx context.Context) (map[string][]FactDetail, error) {
	query := `
		SELECT
		    p.name AS person_name,
		    f.id AS fact_id,
		    f.fact_text,
		    fs.id AS fact_source_id,
		    fs.source_url,
		    fs.original_quote
		FROM
		    persons p
		JOIN
		    fact_mentions fm ON p.id = fm.person_id
		JOIN
		    fact_sources fs ON fm.fact_source_id = fs.id
		JOIN
		    facts f ON fs.fact_id = f.id
		ORDER BY
		    p.name, f.id, fs.id; -- Order ensures facts for a person are together
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to execute grouped facts query: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]FactDetail)
	for rows.Next() {
		var personName string
		var detail FactDetail
		if err := rows.Scan(
			&personName,
			&detail.FactID,
			&detail.FactText,
			&detail.FactSourceID,
			&detail.SourceURL,
			&detail.OriginalQuote,
		); err != nil {
			return nil, fmt.Errorf("failed to scan fact detail row: %w", err)
		}

		// Append the detail to the list for the current person
		result[personName] = append(result[personName], detail)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over grouped facts rows: %w", rows.Err())
	}

	// Optional: Deduplicate FactDetail entries within each person's list if needed.
	// The current query might return the same FactDetail multiple times if a person
	// is mentioned multiple times in relation to the same fact source (unlikely with current schema).
	// Or if multiple people are mentioned in one source, that source detail appears under each person.
	// For simple listing, this is usually acceptable.

	return result, nil
} 