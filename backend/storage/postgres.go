package storage

import (
	"context"
	// "digital-mirror/backend" // Removed unused import
	// Assuming backend.Fact is no longer needed here directly
	"digital-mirror/backend/util" // Import the new util package
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
	pool *pgxpool.Pool
	// embedder embedding.EmbeddingGenerator // REMOVED
	// contradictor contradiction.ContradictionDetector // REMOVED
}

// NewPostgresStorerFromEnv creates a new PostgresStorer by connecting to the database
// specified by the DATABASE_URL environment variable.
func NewPostgresStorerFromEnv(ctx context.Context) (*PostgresStorer, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}

	// REMOVED: embedder validation

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse DATABASE_URL: %w", err)
	}

	// Register pgvector types with the connection config
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		log.Debug().Msg("Registering pgvector types with pgx connection")
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close() // Close pool if ping fails
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	log.Debug().Msg("Successfully connected to database with pgvector type registered.")

	return &PostgresStorer{
		pool: pool,
		// embedder: embedder, // REMOVED
		// contradictor: contradictor, // REMOVED
	}, nil
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
	// L2 distance for normalized vectors. sqrt(2 - 2*cos_sim)
	// 0.6 corresponds to cos_sim ~0.82
	// 0.45 corresponds to cos_sim ~0.9
	// embeddingSimilarityThreshold = 0.15 // Example: requires fairly high similarity (CosSim ~0.99)
	embeddingSimilarityThreshold = 0.6 // Relaxed threshold (CosSim ~0.82)
	// embeddingSimilarityThreshold = 1.0 // Very lenient threshold for debugging (CosSim > 0.5)
	// textSimilarityThreshold could be used with pg_trgm or other text similarity checks if needed.
	// For now, we rely primarily on embedding + exact match on text for simplicity.
	// textSimilarityThreshold = 0.8
	// maxCandidatesToCheck limits how many embedding matches we check text against.
	maxCandidatesToCheck = 5
)

// StoreFact saves a normalized fact, links its source, and connects involved people.
// It NO LONGER checks for embedding similarity for deduplication.
// It inserts the fact and links the source/mentions.
// Contradiction checking is expected to happen *outside* this function using the returned IDs.
func (s *PostgresStorer) StoreFact(ctx context.Context, input StoreFactInput) (factID int64, factSourceID int64, personIDs []int64, err error) {
	// Validate input
	if input.NormalizedFactText == "" || len(input.Embedding) == 0 || input.SourceURL == "" || len(input.InvolvedPeople) == 0 {
		return 0, 0, nil, fmt.Errorf("invalid input: missing required fields (NormalizedFactText, Embedding, SourceURL, InvolvedPeople)")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during transaction: %v", r)
			log.Error().Interface("panic_info", r).Msg("Panic recovered during StoreFact transaction")
		}
		if err != nil {
			log.Error().Err(err).Msg("Rolling back transaction due to error in StoreFact")
			if rbErr := tx.Rollback(ctx); rbErr != nil {
				log.Error().Err(rbErr).Msg("Failed to rollback transaction")
				err = fmt.Errorf("transaction error: %v; rollback error: %w", err, rbErr)
			}
		}
	}()

	// Step 1: Find or Insert Persons first to get their IDs
	personIDs, err = s.findOrInsertPersons(ctx, tx, input.InvolvedPeople, input.NormalizedFactText)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("step 1 failed (findOrInsertPersons): %w", err)
	}
	log.Debug().Ints64("person_ids", personIDs).Msg("Found/inserted persons")

	// Step 2: Insert the new fact
	factID, err = s.insertFact(ctx, tx, input.NormalizedFactText, input.Embedding, input.Topics)
	if err != nil {
		return 0, 0, personIDs, fmt.Errorf("step 2 failed (insertFact): %w", err)
	}
	log.Info().Int64("fact_id", factID).Str("fact_text", input.NormalizedFactText).Msg("Inserted new fact")

	// Step 3: Link Source to the new Fact
	factSourceID, err = s.linkSourceToFact(ctx, tx, factID, input.SourceURL, input.OriginalQuote, input.PublishedAt)
	if err != nil {
		return factID, 0, personIDs, fmt.Errorf("step 3 failed (linkSourceToFact): %w", err)
	}

	// Step 4: Link Persons to *this specific* Source Mention
	err = s.linkPersonsToSourceMention(ctx, tx, factSourceID, personIDs)
	if err != nil {
		return factID, factSourceID, personIDs, fmt.Errorf("step 4 failed (linkPersonsToSourceMention): %w", err)
	}

	// --- Commit ---
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Return the IDs of the newly created fact, source link, and the involved persons
	return factID, factSourceID, personIDs, nil // Success, err is nil
}

// findOrInsertFact REMOVED as StoreFact now always uses insertFact.
// If text-based deduplication is needed later, it can be added back carefully.
/*
func (s *PostgresStorer) findOrInsertFact(...) { ... }
*/

// insertFact inserts a new fact record.
func (s *PostgresStorer) insertFact(ctx context.Context, tx pgx.Tx, text string, embedding []float32, topics []string) (factID int64, err error) {
	insertFactStmt := `
		INSERT INTO facts (fact_text, embedding, topics)
		VALUES ($1, $2, $3)
		RETURNING id`
	err = tx.QueryRow(ctx, insertFactStmt,
		text,
		pgvector.NewVector(embedding),
		topics,
	).Scan(&factID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert new fact: %w", err)
	}
	log.Debug().Int64("new_fact_id", factID).Str("fact_text", text).Msg("Inserted new fact record")
	return factID, nil
}

// Struct to hold similarity search results
type SimilarFactResult struct {
	ID       int64
	Distance float64
}

// FindSimilarFacts searches for facts based on embedding distance
// within a predefined threshold. Operates within a transaction or pool connection.
// Returns a slice of matches found within the threshold, ordered by distance.
// Changed: queryer type to Queryer interface to accept both Tx and Pool
// Changed: Return type to []SimilarFactResult
func (s *PostgresStorer) FindSimilarFacts(ctx context.Context, queryer pgx.Tx, embedding []float32) ([]SimilarFactResult, error) {
	// Use a CTE to calculate distances first
	searchStmt := `
		WITH distances AS (
		  SELECT id, embedding <-> $1 AS dist
		  FROM facts
		)
		SELECT id, dist AS distance
		FROM distances
		WHERE dist <= $3 -- Filter by threshold
		ORDER BY dist   -- Order by calculated distance
		LIMIT $2`
	rows, err := queryer.Query(ctx, searchStmt,
		pgvector.NewVector(embedding),
		maxCandidatesToCheck,         // $2 = LIMIT
		embeddingSimilarityThreshold, // $3 = threshold
	)
	if err != nil {
		// Wrap error for context
		return nil, fmt.Errorf("failed to search for similar facts by embedding: %w", err)
	}
	defer rows.Close()

	var results []SimilarFactResult = make([]SimilarFactResult, 0, maxCandidatesToCheck) // Initialize slice

	for rows.Next() {
		var result SimilarFactResult
		if err = rows.Scan(&result.ID, &result.Distance); err != nil {
			// Wrap error for context
			return nil, fmt.Errorf("failed to scan similar fact candidate: %w", err)
		}
		results = append(results, result) // Add each valid result
	}

	if rows.Err() != nil {
		// Wrap error for context
		return nil, fmt.Errorf("error iterating through similar fact candidates: %w", rows.Err())
	}

	// Return the slice of results (might be empty)
	return results, nil
}

// findOrInsertPersons finds existing persons or inserts new ones based on normalized names using batch operations.
// Returns a slice of database IDs corresponding to the input personNames.
func (s *PostgresStorer) findOrInsertPersons(ctx context.Context, tx pgx.Tx, personNames []string, factTextForLogging string) (personIDs []int64, err error) {
	if len(personNames) == 0 {
		return []int64{}, nil // Nothing to do
	}

	// 1. Normalize names and create maps for tracking
	normalizedNameToOriginal := make(map[string]string)
	normalizedNamesList := make([]string, 0, len(personNames))
	uniqueNormalizedNames := make(map[string]struct{}) // Using struct{} as a lightweight set value

	for _, originalName := range personNames {
		normalized := util.NormalizeString(originalName)
		if normalized == "" {
			log.Warn().Str("original_name", originalName).Str("related_fact_text", factTextForLogging).Msg("Skipping empty person name during find/insert")
			continue
		}
		if _, exists := uniqueNormalizedNames[normalized]; !exists {
			uniqueNormalizedNames[normalized] = struct{}{}
			normalizedNamesList = append(normalizedNamesList, normalized)
			normalizedNameToOriginal[normalized] = originalName // Keep track for logging/errors
		}
	}

	if len(normalizedNamesList) == 0 {
		return nil, fmt.Errorf("no valid persons associated with fact after normalization: '%s'", factTextForLogging)
	}

	// 2. Find existing persons
	existingPersons := make(map[string]int64) // Map normalized name -> existing ID
	findStmt := `SELECT id, name FROM persons WHERE name = ANY($1)`
	rows, err := tx.Query(ctx, findStmt, normalizedNamesList)
	if err != nil {
		return nil, fmt.Errorf("failed to query existing persons: %w", err)
	}
	defer rows.Close() // Ensure rows are closed even if scanning fails mid-way

	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close() // Close immediately on error
			return nil, fmt.Errorf("failed to scan existing person row: %w", err)
		}
		existingPersons[name] = id
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating existing person rows: %w", rows.Err())
	}
	rows.Close() // Explicit close after successful iteration

	// 3. Determine which persons need insertion
	personsToInsert := make([][]any, 0)
	finalPersonIDsMap := make(map[string]int64) // Store all IDs (existing + new) by normalized name
	for _, normalizedName := range normalizedNamesList {
		if id, exists := existingPersons[normalizedName]; exists {
			finalPersonIDsMap[normalizedName] = id
		} else {
			personsToInsert = append(personsToInsert, []any{normalizedName})
		}
	}

	// 4. Batch insert missing persons using CopyFrom
	if len(personsToInsert) > 0 {
		copyCount, err := tx.CopyFrom(
			ctx,
			pgx.Identifier{"persons"},         // Table name
			[]string{"name"},                  // Column names
			pgx.CopyFromRows(personsToInsert), // Source data
		)
		if err != nil {
			return nil, fmt.Errorf("failed to batch insert new persons using CopyFrom: %w", err)
		}
		if int(copyCount) != len(personsToInsert) {
			log.Warn().Int64("expected_inserts", int64(len(personsToInsert))).Int64("actual_inserts", copyCount).Msg("CopyFrom inserted unexpected number of person rows")
			// This might happen if there's a race condition or unexpected DB state, but proceed cautiously.
		}
		log.Debug().Int64("insert_count", copyCount).Msg("Batch inserted new persons")

		// 5. Get IDs of newly inserted persons
		newlyInsertedNames := make([]string, len(personsToInsert))
		for i, data := range personsToInsert {
			newlyInsertedNames[i] = data[0].(string)
		}

		findNewStmt := `SELECT id, name FROM persons WHERE name = ANY($1)`
		newRows, err := tx.Query(ctx, findNewStmt, newlyInsertedNames)
		if err != nil {
			return nil, fmt.Errorf("failed to query newly inserted persons: %w", err)
		}
		defer newRows.Close()

		for newRows.Next() {
			var id int64
			var name string
			if err := newRows.Scan(&id, &name); err != nil {
				newRows.Close()
				return nil, fmt.Errorf("failed to scan newly inserted person row: %w", err)
			}
			finalPersonIDsMap[name] = id // Add newly inserted IDs
		}
		if newRows.Err() != nil {
			return nil, fmt.Errorf("error iterating newly inserted person rows: %w", newRows.Err())
		}
		newRows.Close()
	}

	// 6. Collect final list of IDs in the original order of unique normalized names
	personIDs = make([]int64, 0, len(normalizedNamesList))
	for _, normalizedName := range normalizedNamesList {
		if id, ok := finalPersonIDsMap[normalizedName]; ok {
			personIDs = append(personIDs, id)
		} else {
			// This should not happen if logic is correct
			log.Error().Str("normalized_name", normalizedName).Msg("Could not find final ID for person after insert/query steps")
			return nil, fmt.Errorf("internal error: could not find final ID for person '%s'", normalizedNameToOriginal[normalizedName])
		}
	}

	return personIDs, nil
}

// linkSourceToFact links a source URL, quote, and published date to a given fact ID.
// If the source URL already exists, it updates its last_visited_at timestamp.
// Otherwise, it inserts a new source record with last_visited_at set to current time.
func (s *PostgresStorer) linkSourceToFact(ctx context.Context, tx pgx.Tx, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time) (factSourceID int64, err error) {
	// Use COALESCE for published_at to handle nil case properly in SQL
	// Update last_visited_at on conflict
	insertSourceStmt := `
		INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at, last_visited_at)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)
		ON CONFLICT (source_url) DO UPDATE
		SET fact_id = EXCLUDED.fact_id, 
		    original_quote = EXCLUDED.original_quote, 
		    published_at = EXCLUDED.published_at,
		    last_visited_at = CURRENT_TIMESTAMP
		RETURNING id`
	// This assumes that if a URL conflicts, we still want to associate it with the *current* factID and details.
	// If the policy should be different (e.g., only update last_visited_at but keep old fact_id/quote), the ON CONFLICT clause needs adjustment.
	// For now, the logic is to "refresh" the source details if re-linked.

	var publishedAtVal interface{}
	if publishedAt != nil {
		publishedAtVal = *publishedAt
	} else {
		publishedAtVal = nil // Explicitly pgx.Null/nil for database
	}

	err = tx.QueryRow(ctx, insertSourceStmt,
		factID,
		sourceURL,
		originalQuote,
		publishedAtVal,
	).Scan(&factSourceID)

	if err != nil {
		return 0, fmt.Errorf("failed to insert or update fact source for URL '%s': %w", sourceURL, err)
	}
	log.Debug().Int64("fact_source_id", factSourceID).Str("source_url", sourceURL).Msg("Inserted or updated fact source and updated last_visited_at")
	return factSourceID, nil
}

// linkPersonsToSourceMention links persons to a specific fact source mention using a batch operation.
func (s *PostgresStorer) linkPersonsToSourceMention(ctx context.Context, tx pgx.Tx, factSourceID int64, personIDs []int64) (err error) {
	batch := &pgx.Batch{}
	mentionInsertStmt := `
		INSERT INTO fact_mentions (fact_source_id, person_id)
		VALUES ($1, $2)
		ON CONFLICT (fact_source_id, person_id) DO NOTHING` // Silently ignore duplicates here

	// Queue an insert command for each person ID
	for _, personID := range personIDs {
		batch.Queue(mentionInsertStmt, factSourceID, personID)
	}

	// Send the batch
	br := tx.SendBatch(ctx, batch)
	defer br.Close() // Ensure batch results are closed

	// Check results for each queued command (optional but good practice)
	for range personIDs {
		cmdTag, err := br.Exec()
		if err != nil {
			// Attempt to get more specific error context if possible
			// For now, return a general error indicating batch failure
			log.Error().Err(err).Int64("fact_source_id", factSourceID).Msg("Error executing batch insert for fact_mentions")
			return fmt.Errorf("failed during batch insert for mentions (fact source %d): %w", factSourceID, err)
		}
		// Optionally log rows affected, though ON CONFLICT DO NOTHING means it could be 0
		_ = cmdTag // Avoid unused variable warning
		// log.Debug().Int64("rows_affected", cmdTag.RowsAffected()).Msg("Mention insert command executed in batch")
	}

	// Close the batch results reader explicitly before returning
	if err := br.Close(); err != nil {
		log.Error().Err(err).Msg("Error closing batch results for fact_mentions insert")
		return fmt.Errorf("failed to close batch results for mentions insert: %w", err)
	}

	log.Debug().Int64("fact_source_id", factSourceID).Int("mention_count", len(personIDs)).Msg("Successfully linked persons to source mention via batch")
	return nil // Success
}

// --- Add methods for URL pre-check if desired ---

// IsURLVisitable checks if a given URL should be visited based on the last visit time and a configurable interval.
// It returns:
// 1. visitable (bool): True if the URL can be visited (either new or past the revisit interval).
// 2. sourceExists (bool): True if the URL already exists in the fact_sources table.
// 3. err (error): Any error encountered during the database query.
func (s *PostgresStorer) IsURLVisitable(ctx context.Context, url string, revisitIntervalHours int) (visitable bool, sourceExists bool, err error) {
	var lastVisitedAt time.Time
	query := `SELECT last_visited_at FROM fact_sources WHERE source_url = $1`

	err = s.pool.QueryRow(ctx, query, url).Scan(&lastVisitedAt)

	if err == pgx.ErrNoRows {
		// URL does not exist, so it's visitable and sourceExists is false
		return true, false, nil
	}
	if err != nil {
		// Other query error
		return false, false, fmt.Errorf("failed to query last_visited_at for URL '%s': %w", url, err)
	}

	// URL exists, sourceExists is true
	sourceExists = true

	// Check if last_visited_at is older than NOW() - revisitIntervalHours
	if time.Since(lastVisitedAt) > (time.Duration(revisitIntervalHours) * time.Hour) {
		visitable = true // It's time to revisit
	} else {
		visitable = false // Visited too recently
	}

	return visitable, sourceExists, nil
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

// LinkExistingFactToSourceInput encapsulates data needed to link a new source to an existing fact.
// Simplified version: only links the source info, not people specific to this source mention.
type LinkExistingFactToSourceInput struct {
	ExistingFactID int64      // The ID of the fact already in the database
	SourceURL      string     // The URL of the new source
	OriginalQuote  string     // The quote from the new source
	PublishedAt    *time.Time // Publication date of the new source
	// InvolvedPeople []string   // Removed: Assumes people are linked to the core fact, not each source mention
}

// LinkExistingFactToSource links a new source URL, quote, and published date
// to a fact that already exists in the database. (Simplified version)
func (s *PostgresStorer) LinkExistingFactToSource(ctx context.Context, input LinkExistingFactToSourceInput) (factSourceID int64, err error) {
	// Validate input
	// if input.ExistingFactID <= 0 || input.SourceURL == "" || len(input.InvolvedPeople) == 0 { // Old validation
	if input.ExistingFactID <= 0 || input.SourceURL == "" { // Simplified validation
		// return 0, fmt.Errorf("invalid input: missing required fields (ExistingFactID, SourceURL, InvolvedPeople)") // Old error
		return 0, fmt.Errorf("invalid input: missing required fields (ExistingFactID, SourceURL)") // Simplified error
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during transaction: %v", r)
			log.Error().Interface("panic_info", r).Msg("Panic recovered during LinkExistingFactToSource transaction")
		}
		if err != nil {
			log.Error().Err(err).Msg("Rolling back transaction due to error in LinkExistingFactToSource")
			if rbErr := tx.Rollback(ctx); rbErr != nil {
				log.Error().Err(rbErr).Msg("Failed to rollback transaction")
				err = fmt.Errorf("transaction error: %v; rollback error: %w", err, rbErr)
			}
		}
	}()

	// // Step B (modified): Find or Insert Persons mentioned in *this specific quote* // REMOVED
	// personIDs, err := s.findOrInsertPersons(ctx, tx, input.InvolvedPeople, fmt.Sprintf("Source: %s", input.SourceURL)) // Use URL for logging context
	// if err != nil {
	// 	return 0, fmt.Errorf("step B failed (findOrInsertPersons for existing fact link): %w", err)
	// }

	// Step C (modified): Link Source to the *Existing* Fact ID
	factSourceID, err = s.linkSourceToFact(ctx, tx, input.ExistingFactID, input.SourceURL, input.OriginalQuote, input.PublishedAt)
	if err != nil {
		// return 0, fmt.Errorf("step C failed (linkSourceToFact for existing fact): %w", err) // Old error step label
		return 0, fmt.Errorf("failed to link source to existing fact: %w", err) // Simplified error
	}

	// // Step D (modified): Link Persons to *this specific* Source Mention // REMOVED
	// err = s.linkPersonsToSourceMention(ctx, tx, factSourceID, personIDs)
	// if err != nil {
	// 	return 0, fmt.Errorf("step D failed (linkPersonsToSourceMention for existing fact): %w", err)
	// }

	// --- Commit ---
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction for LinkExistingFactToSource: %w", err)
	}

	log.Info().Int64("fact_id", input.ExistingFactID).Int64("fact_source_id", factSourceID).Str("source_url", input.SourceURL).Msg("Successfully linked new source to existing fact")
	return factSourceID, nil // Success, err is nil
}

// Pool returns the underlying database connection pool.
// This is useful for operations outside the main StoreFact/Link methods,
// like the similarity check in the cron job.
func (s *PostgresStorer) Pool() *pgxpool.Pool {
	return s.pool
}

// FindNeighborFactsForPerson searches for facts linked to a specific person that are
// semantically similar to a given embedding, excluding a specific fact ID (usually the new one).
// Returns IDs and text of neighboring facts within the similarity threshold.
type NeighborFactResult struct {
	ID       int64
	Text     string
	Distance float64 // Include distance for potential future use/logging
}

func (s *PostgresStorer) FindNeighborFactsForPerson(ctx context.Context, queryer pgx.Tx, personID int64, embedding []float32, excludeFactID int64) ([]NeighborFactResult, error) {
	query := `
        WITH PersonFacts AS (
            -- Select distinct fact IDs associated with the person
            SELECT DISTINCT fs.fact_id
            FROM fact_mentions fm
            JOIN fact_sources fs ON fm.fact_source_id = fs.id
            WHERE fm.person_id = $1
        )
        SELECT
            f.id,
            f.fact_text,
            f.embedding <-> $2 AS distance
        FROM facts f
        JOIN PersonFacts pf ON f.id = pf.fact_id
        WHERE
            f.id <> $3 -- Exclude the fact itself
        AND
            f.embedding <-> $2 <= $4 -- Similarity threshold
        ORDER BY
            distance -- Order by similarity
        LIMIT $5`

	rows, err := queryer.Query(ctx, query,
		personID,                      // $1
		pgvector.NewVector(embedding), // $2
		excludeFactID,                 // $3
		embeddingSimilarityThreshold,  // $4
		maxCandidatesToCheck,          // $5
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query neighbor facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	var results []NeighborFactResult
	for rows.Next() {
		var res NeighborFactResult
		if err := rows.Scan(&res.ID, &res.Text, &res.Distance); err != nil {
			return nil, fmt.Errorf("failed to scan neighbor fact result for person %d: %w", personID, err)
		}
		results = append(results, res)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating neighbor fact results for person %d: %w", personID, rows.Err())
	}

	return results, nil
}

// RecordContradiction inserts a record indicating that two facts for a person are contradictory.
// Ensures that factAID < factBID to avoid duplicate pairs in different orders.
// Now includes a reason for the contradiction.
func (s *PostgresStorer) RecordContradiction(ctx context.Context, tx pgx.Tx, personID int64, factAID int64, factBID int64, reason string) error {
	// Ensure consistent ordering of fact IDs to prevent duplicates like (1,2) and (2,1)
	if factAID > factBID {
		factAID, factBID = factBID, factAID
	}

	query := `
        INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
        SET reason = EXCLUDED.reason, -- Update reason if the pair is reported again
            detected_at = CURRENT_TIMESTAMP`

	cmdTag, err := tx.Exec(ctx, query, personID, factAID, factBID, reason)
	if err != nil {
		return fmt.Errorf("failed to insert/update contradiction for person %d between facts %d and %d: %w", personID, factAID, factBID, err)
	}

	if cmdTag.RowsAffected() > 0 {
		log.Info().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).Msg("Recorded/updated contradiction")
	}

	return nil
}

// GetFactTextByID retrieves the normalized text of a specific fact by its ID.
// Operates within a given transaction.
func (s *PostgresStorer) GetFactTextByID(ctx context.Context, tx pgx.Tx, factID int64) (string, error) {
	var factText string
	query := `SELECT fact_text FROM facts WHERE id = $1`
	err := tx.QueryRow(ctx, query, factID).Scan(&factText)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Consider if ErrNoRows should be a distinct error type or just logged
			return "", fmt.Errorf("fact not found with ID %d: %w", factID, err)
		}
		return "", fmt.Errorf("failed to query fact text for ID %d: %w", factID, err)
	}
	return factText, nil
}

// GetFactsForPerson retrieves all facts and their source details for a specific person.
func (s *PostgresStorer) GetFactsForPerson(ctx context.Context, personName string) ([]FactDetail, error) {
	var details []FactDetail

	// First, get the person_id for the given name
	var personID int64
	findPersonIDStmt := `SELECT id FROM persons WHERE name = $1`
	err := s.pool.QueryRow(ctx, findPersonIDStmt, personName).Scan(&personID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("person with name '%s' not found", personName)
		}
		return nil, fmt.Errorf("failed to find person_id for '%s': %w", personName, err)
	}

	query := `
		SELECT
			f.id AS fact_id,
			f.fact_text,
			fs.id AS fact_source_id,
			fs.source_url,
			fs.original_quote
		FROM
			facts f
		JOIN
			fact_sources fs ON f.id = fs.fact_id
		JOIN
			fact_mentions fm ON fs.id = fm.fact_source_id
		WHERE
			fm.person_id = $1
		ORDER BY
			f.fact_text, fs.source_url; -- Consistent ordering
	`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for person_id %d ('%s'): %w", personID, personName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var detail FactDetail
		if err := rows.Scan(&detail.FactID, &detail.FactText, &detail.FactSourceID, &detail.SourceURL, &detail.OriginalQuote); err != nil {
			return nil, fmt.Errorf("failed to scan fact detail for person_id %d: %w", personID, err)
		}
		details = append(details, detail)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating fact details for person_id %d: %w", personID, rows.Err())
	}

	log.Info().Str("person_name", personName).Int("fact_count", len(details)).Msg("Successfully retrieved facts for person")
	return details, nil
}
