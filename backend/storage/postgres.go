package storage

import (
	"context"
	"encoding/json"
	// "digital-mirror/backend" // Removed unused import
	// Assuming backend.Fact is no longer needed here directly
	"digital-mirror/backend/core"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util" // Import the new util package
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/rs/zerolog/log" // Import zerolog
)

// --- Structs for Storing ---

// Affiliation represents a temporal relationship between a person and a party
type Affiliation struct {
	PartyID    int64
	StartTime  *time.Time
	EndTime    *time.Time
	Attributes map[string]any
}

// StoreFactInput encapsulates the data needed to store a fact and its source.
// This replaces the direct use of backend.Fact in the storage layer.
type StoreFactInput struct {
	// SubjectEntity is the primary entity this fact is about (required).
	// This is used as the primary person link. InvolvedPeople are secondary mentions.
	SubjectEntity string
	// SubjectEntityType is the type of entity: "human", "party", or "organization".
	// Used when creating new persons in the database.
	SubjectEntityType string
	// NormalizedFactText is the core, normalized factual statement (with person names for display).
	NormalizedFactText string
	// EmbeddingText is the name-free version used ONLY for generating embeddings.
	// This ensures facts cluster by topic, not by person name.
	EmbeddingText string
	// Embedding is the vector embedding generated from EmbeddingText.
	Embedding []float32
	// OriginalQuote is the exact text snippet from the source.
	OriginalQuote string
	// CanonicalVerb is one of 20 canonical verbs for verb-aware filtering.
	CanonicalVerb string
	// InvolvedPeople lists secondary people mentioned in the fact (not the subject entity).
	InvolvedPeople []string
	// SourceURL is the URL where the statement was found.
	SourceURL string
	// PublishedAt is the publication date of the source content, if available.
	PublishedAt *time.Time
	// SourceType is an optional classification tag for observability (e.g., "web", "twitter", "pdf").
	SourceType string
	// Metadata is optional JSON-encoded plugin-specific data about the source (e.g., tweet_id).
	Metadata map[string]any
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

	// Configure connection pool for long-running operations (like LLM calls)
	config.MaxConnLifetime = 30 * time.Minute      // Recycle connections periodically
	config.MaxConnIdleTime = 5 * time.Minute       // Close idle connections before server times them out
	config.HealthCheckPeriod = 30 * time.Second    // Check connection health periodically
	config.MaxConns = 10                           // Reasonable pool size
	config.MinConns = 2                            // Keep some connections warm

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

	// Start pool stats collector for Prometheus metrics (every 15 seconds)
	metrics.StartPoolStatsCollector(ctx, pool, 15*time.Second)

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

// GetAffiliatedPersonIDs returns the dst_person_id(s) for a given src_person_id
// filtered by link_type='affiliation' and optionally by an onDate (published_at).
// The queryer parameter is a tx or pool connection supporting Query.
func (s *PostgresStorer) GetAffiliatedPersonIDs(ctx context.Context, queryer pgx.Tx, srcPersonID int64, onDate *time.Time) ([]int64, error) {
	query := `SELECT dst_person_id FROM person_links
			  WHERE src_person_id = $1
				AND link_type = 'affiliation'
				AND ($2::date IS NULL OR (valid_from IS NULL OR valid_from <= $2::date))
				AND ($2::date IS NULL OR (valid_to   IS NULL OR valid_to   >= $2::date))`

	rows, err := queryer.Query(ctx, query, srcPersonID, onDate)
	if err != nil {
		return nil, fmt.Errorf("failed to query affiliations for person %d: %w", srcPersonID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var dst int64
		if err := rows.Scan(&dst); err != nil {
			return nil, fmt.Errorf("failed to scan affiliation row for person %d: %w", srcPersonID, err)
		}
		out = append(out, dst)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating affiliation rows for person %d: %w", srcPersonID, rows.Err())
	}
	return out, nil
}

// GetPersonIDByAssembleeID returns the person.id associated with the given assemblee_id in persons.attributes.
func (s *PostgresStorer) GetPersonIDByAssembleeID(ctx context.Context, assembleeID string) (int64, error) {
	var id int64
	query := `SELECT id FROM persons WHERE attributes->>'assemblee_id' = $1 LIMIT 1`
	err := s.pool.QueryRow(ctx, query, assembleeID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to find person by assemblee_id %s: %w", assembleeID, err)
	}
	return id, nil
}

// GetPersonIDByOrgRef returns the person.id associated with the given orgRef stored in persons.attributes.
func (s *PostgresStorer) GetPersonIDByOrgRef(ctx context.Context, orgRef string) (int64, error) {
	var id int64
	query := `SELECT id FROM persons WHERE attributes->>'organeRef' = $1 LIMIT 1`
	err := s.pool.QueryRow(ctx, query, orgRef).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to find person by orgRef %s: %w", orgRef, err)
	}
	return id, nil
}

// GetPersonIDByName returns the person.id associated with the given name.
func (s *PostgresStorer) GetPersonIDByName(ctx context.Context, name string) (int64, error) {
	var id int64
	query := `SELECT id FROM persons WHERE name = $1 LIMIT 1`
	err := s.pool.QueryRow(ctx, query, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to find person by name %s: %w", name, err)
	}
	return id, nil
}

// FindPersonsByFuzzyName finds persons matching the query using fuzzy matching.
// Returns matches sorted by similarity score (highest first).
// threshold is the minimum similarity score (0.0 to 1.0), recommended: 0.6
// maxResults limits the number of results returned
func (s *PostgresStorer) FindPersonsByFuzzyName(ctx context.Context, query string, threshold float64, maxResults int) ([]util.PersonMatch, error) {
	// Get all persons
	allPersons, err := s.GetAllPersons(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all persons for fuzzy search: %w", err)
	}

	// Convert to format expected by FindBestMatches
	candidates := make([]struct {
		ID         int64
		Name       string
		EntityType string
	}, len(allPersons))

	for i, p := range allPersons {
		candidates[i] = struct {
			ID         int64
			Name       string
			EntityType string
		}{
			ID:         p.ID,
			Name:       p.Name,
			EntityType: p.EntityType,
		}
	}

	// Find matches
	matches := util.FindBestMatches(query, candidates, threshold, maxResults)

	return matches, nil
}

// FindOrCreateAffiliation inserts or updates an affiliation (one-way) between two persons.
// It uses link_type='affiliation' and is idempotent.
func (s *PostgresStorer) FindOrCreateAffiliation(ctx context.Context, tx pgx.Tx, srcID int64, dstID int64, validFrom *time.Time, validTo *time.Time, attributes map[string]any) error {
	stmt := `
		INSERT INTO person_links (src_person_id, dst_person_id, link_type, valid_from, valid_to, attributes)
		VALUES ($1, $2, 'affiliation', $3, $4, $5)
		ON CONFLICT (src_person_id, dst_person_id, link_type) DO UPDATE
		SET
			valid_from = COALESCE(person_links.valid_from, EXCLUDED.valid_from),
			valid_to   = COALESCE(EXCLUDED.valid_to, person_links.valid_to),
			attributes = person_links.attributes || COALESCE(EXCLUDED.attributes, '{}'::jsonb)
		RETURNING id`

	var attrVal interface{}
	if attributes != nil {
		attrVal = attributes
	} else {
		attrVal = nil
	}
	var id string
	err := tx.QueryRow(ctx, stmt, srcID, dstID, validFrom, validTo, attrVal).Scan(&id)
	if err != nil {
		return fmt.Errorf("failed to upsert affiliation for src=%d dst=%d: %w", srcID, dstID, err)
	}
	return nil
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
	embeddingSimilarityThreshold = 0.8 // Relaxed threshold for text-embedding-3-small (CosSim ~0.68)
	// embeddingSimilarityThreshold = 1.0 // Very lenient threshold for debugging (CosSim > 0.5)
	// textSimilarityThreshold could be used with pg_trgm or other text similarity checks if needed.
	// For now, we rely primarily on embedding + exact match on text for simplicity.
	// textSimilarityThreshold = 0.8
	// maxCandidatesToCheck limits how many embedding matches we check text against.
	// Higher values ensure more pairs are checked for contradictions (at cost of more LLM calls)
	maxCandidatesToCheck = 20
)

// REMOVED: StoreFact() has been replaced by core.FactInserter
// Use core.NewFactInserter(storer, nil).InsertFacts() instead for intelligent fact insertion
// with automatic similarity detection and canonical/related fact management.
//
// Migration guide:
// OLD: factID, sourceID, personIDs, err := storer.StoreFact(ctx, input)
// NEW: inserter := core.NewFactInserter(storer, nil)
//      err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
//
// Note: The new approach handles similarity detection automatically and stores
// facts as either canonical or related based on embedding similarity.

// --- Public methods for core.FactInserter ---

// FindOrInsertPersons is a public wrapper around findOrInsertPersons for use by core.FactInserter.
// It finds existing persons or inserts new ones based on normalized names.
// Returns a slice of database IDs corresponding to the input personNames.
func (s *PostgresStorer) FindOrInsertPersons(ctx context.Context, personNames []string) ([]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) // Rollback if not committed

	personIDs, err := s.findOrInsertPersons(ctx, tx, personNames, "")
	if err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return personIDs, nil
}

// FindOrInsertPersonWithType finds or creates a person with the specified entity type.
// If the person already exists, returns their ID (does NOT update entity_type for existing persons).
// If the person doesn't exist, creates them with the specified entity type.
// entityType must be one of: "human", "party", "organization".
func (s *PostgresStorer) FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error) {
	normalized := util.NormalizeString(name)
	if normalized == "" {
		return 0, fmt.Errorf("empty person name after normalization")
	}

	// Validate entity type, default to "human" if invalid
	switch entityType {
	case "human", "party", "organization":
		// valid
	default:
		log.Warn().Str("entity_type", entityType).Str("name", name).Msg("Invalid entity_type, defaulting to 'human'")
		entityType = "human"
	}

	// Try to find existing person first
	var personID int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM persons WHERE name = $1`, normalized).Scan(&personID)
	if err == nil {
		return personID, nil
	}
	if err != pgx.ErrNoRows {
		return 0, fmt.Errorf("failed to query person: %w", err)
	}

	// Person doesn't exist, insert with entity type
	err = s.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ($1, $2)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`,
		normalized, entityType,
	).Scan(&personID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert person with entity type: %w", err)
	}

	log.Info().
		Int64("person_id", personID).
		Str("name", name).
		Str("normalized_name", normalized).
		Str("entity_type", entityType).
		Msg("Created new person with entity type")

	return personID, nil
}

// LinkSourceToFact is a public wrapper around linkSourceToFact for use by core.FactInserter.
// It links a source URL, quote, and published date to a given fact ID.
func (s *PostgresStorer) LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) // Rollback if not committed

	sourceID, err := s.linkSourceToFact(ctx, tx, factID, sourceURL, originalQuote, publishedAt, sourceType, metadata)
	if err != nil {
		return 0, err
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return sourceID, nil
}

// LinkPersonsToSourceMention is a public wrapper around linkPersonsToSourceMention for use by core.FactInserter.
// It links persons to a specific fact source mention.
func (s *PostgresStorer) LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) // Rollback if not committed

	if err := s.linkPersonsToSourceMention(ctx, tx, sourceID, personIDs); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// FindSimilarActiveFacts searches for active (non-duplicate) facts similar to the given embedding
// for the specified persons, using cosine distance threshold.
// threshold parameter is cosine distance (1 - cosine_similarity), e.g., 0.20 for 80% similarity.
// Excludes facts that are the specific side of a 'duplicate' relation in fact_relations.
// Returns up to limit results (default 20).
func (s *PostgresStorer) FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, limit int) ([]core.SimilarFact, error) {
	timer := metrics.NewQueryTimer("find_similar_active_facts")
	start := time.Now()

	if limit <= 0 {
		limit = 20
	}

	query := `
		WITH PersonFacts AS (
			SELECT DISTINCT fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = ANY($1)
		)
		SELECT f.id, f.fact_text, f.embedding <=> $2 AS distance
		FROM facts f
		JOIN PersonFacts pf ON f.id = pf.fact_id
		WHERE f.embedding <=> $2 <= $3
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY distance
		LIMIT $4
	`

	rows, err := s.pool.Query(ctx, query,
		personIDs,
		pgvector.NewVector(embedding),
		threshold,
		limit,
	)
	if err != nil {
		timer.ObserveDuration(err)
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var results []core.SimilarFact
	for rows.Next() {
		var sf core.SimilarFact
		if err := rows.Scan(&sf.ID, &sf.Text, &sf.Distance); err != nil {
			timer.ObserveDuration(err)
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		results = append(results, sf)
	}

	err = rows.Err()
	timer.ObserveDuration(err)
	metrics.ObserveVectorSearch(time.Since(start), len(results))

	return results, err
}

// InsertFact inserts a new fact into the facts table.
// All facts are inserted uniformly - duplicate/implication relationships are tracked in fact_relations.
func (s *PostgresStorer) InsertFact(ctx context.Context, input core.StoreFactInput) (int64, error) {
	timer := metrics.NewQueryTimer("insert_fact")

	query := `
		INSERT INTO facts (fact_text, embedding_text, embedding, canonical_verb)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`
	var factID int64
	err := s.pool.QueryRow(ctx, query,
		input.NormalizedFactText,
		input.EmbeddingText,
		pgvector.NewVector(input.Embedding),
		input.CanonicalVerb,
	).Scan(&factID)

	timer.ObserveDuration(err)

	if err != nil {
		return 0, fmt.Errorf("failed to insert fact: %w", err)
	}

	log.Debug().Int64("fact_id", factID).Str("fact_text", input.NormalizedFactText[:min(len(input.NormalizedFactText), 80)]).Msg("Inserted fact")
	return factID, nil
}

// GetFactCanonicalVerb returns the canonical verb for a given fact ID.
func (s *PostgresStorer) GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error) {
	var verb string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(canonical_verb, '') FROM facts WHERE id = $1`, factID).Scan(&verb)
	if err != nil {
		return "", fmt.Errorf("failed to get canonical verb for fact %d: %w", factID, err)
	}
	return verb, nil
}

// --- End of public methods for core.FactInserter ---

// findOrInsertFact REMOVED as StoreFact now always uses insertFact.
// If text-based deduplication is needed later, it can be added back carefully.
/*
func (s *PostgresStorer) findOrInsertFact(...) { ... }
*/

// insertFact inserts a new fact record.
// If a fact with the same text already exists, it returns the existing fact ID.
func (s *PostgresStorer) insertFact(ctx context.Context, tx pgx.Tx, text string, embedding []float32, canonicalVerb string) (factID int64, err error) {
	// Use ON CONFLICT to handle duplicate fact_text gracefully
	// DO UPDATE with a no-op update allows us to get the RETURNING id for both new and existing facts
	insertFactStmt := `
		INSERT INTO facts (fact_text, embedding, canonical_verb)
		VALUES ($1, $2, $3)
		ON CONFLICT (fact_text) DO UPDATE SET fact_text = EXCLUDED.fact_text
		RETURNING id`
	err = tx.QueryRow(ctx, insertFactStmt,
		text,
		pgvector.NewVector(embedding),
		canonicalVerb,
	).Scan(&factID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert new fact: %w", err)
	}
	// Suppress per-item debug log to keep CLI output concise
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
func (s *PostgresStorer) linkSourceToFact(ctx context.Context, tx pgx.Tx, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (factSourceID int64, err error) {
	// Use COALESCE for published_at to handle nil case properly in SQL
	// Update last_visited_at on conflict
	insertSourceStmt := `
		INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at, last_visited_at, source_type, metadata)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP, $5, $6)
		ON CONFLICT (fact_id, source_url) DO UPDATE
		SET original_quote = EXCLUDED.original_quote,
		    published_at = EXCLUDED.published_at,
		    last_visited_at = CURRENT_TIMESTAMP,
		    source_type = EXCLUDED.source_type,
		    metadata = COALESCE(EXCLUDED.metadata, fact_sources.metadata)
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

	var metadataJSON any
	if metadata != nil {
		metadataJSON = metadata
	} else {
		metadataJSON = nil
	}

	err = tx.QueryRow(ctx, insertSourceStmt,
		factID,
		sourceURL,
		originalQuote,
		publishedAtVal,
		sourceType,
		metadataJSON,
	).Scan(&factSourceID)

	if err != nil {
		return 0, fmt.Errorf("failed to insert or update fact source for URL '%s': %w", sourceURL, err)
	}
	// Suppress per-item debug log to keep CLI output concise
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

	// Suppress per-item debug log to keep CLI output concise
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

// IsLawKeyProcessed checks if a law (identified by law_key in metadata) has already been processed.
// Returns true if the law_key exists in fact_sources metadata, false otherwise.
// DEPRECATED: Use IsLawFullyProcessed instead for accurate resumability.
func (s *PostgresStorer) IsLawKeyProcessed(ctx context.Context, lawKey string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM fact_sources WHERE metadata->>'law_key' = $1)`
	var exists bool
	err := s.pool.QueryRow(ctx, query, lawKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if law_key '%s' is processed: %w", lawKey, err)
	}
	return exists, nil
}

// IsLawFullyProcessed checks if a law has been completely processed (all facts stored).
// This replaces the old IsLawKeyProcessed which checked fact_sources and could give false positives.
func (s *PostgresStorer) IsLawFullyProcessed(ctx context.Context, lawKey string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM processed_laws WHERE law_key = $1)`
	var exists bool
	err := s.pool.QueryRow(ctx, query, lawKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if law %s is processed: %w", lawKey, err)
	}
	return exists, nil
}

// MarkLawAsProcessed records that a law has been fully processed (all facts stored).
// This is the "commit" marker that prevents reprocessing the law on retry.
func (s *PostgresStorer) MarkLawAsProcessed(ctx context.Context, lawKey string, legislature int, title string, factsCount int) error {
	query := `
		INSERT INTO processed_laws (law_key, legislature, title, facts_count)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (law_key) DO UPDATE
		SET processed_at = CURRENT_TIMESTAMP,
		    facts_count = EXCLUDED.facts_count
	`
	_, err := s.pool.Exec(ctx, query, lawKey, legislature, title, factsCount)
	if err != nil {
		return fmt.Errorf("failed to mark law %s as processed: %w", lawKey, err)
	}
	return nil
}

// ProcessedLawInfo represents metadata about a fully processed law.
type ProcessedLawInfo struct {
	LawKey      string
	Legislature int
	Title       string
	FactsCount  int
	ProcessedAt time.Time
}

// GetProcessedLawInfo retrieves metadata about a processed law (for debugging/verification).
// Returns nil if the law has not been processed.
func (s *PostgresStorer) GetProcessedLawInfo(ctx context.Context, lawKey string) (*ProcessedLawInfo, error) {
	query := `
		SELECT law_key, legislature, title, facts_count, processed_at
		FROM processed_laws
		WHERE law_key = $1
	`
	var info ProcessedLawInfo
	err := s.pool.QueryRow(ctx, query, lawKey).Scan(
		&info.LawKey, &info.Legislature, &info.Title, &info.FactsCount, &info.ProcessedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get processed law info for %s: %w", lawKey, err)
	}
	return &info, nil
}

// DeletePartialLawData removes all facts, fact_sources, contradictions, and fact_mentions
// associated with a law_key. This is used for cleanup before reprocessing an incomplete law.
// IMPORTANT: Only deletes if the law is NOT marked as fully processed in processed_laws table.
// Returns the number of facts deleted.
func (s *PostgresStorer) DeletePartialLawData(ctx context.Context, lawKey string) (int, error) {
	// Use a transaction to ensure atomic cleanup
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Safety check: Only delete if law is NOT in processed_laws table
	var isComplete bool
	checkQuery := `SELECT EXISTS(SELECT 1 FROM processed_laws WHERE law_key = $1)`
	if err := tx.QueryRow(ctx, checkQuery, lawKey).Scan(&isComplete); err != nil {
		return 0, fmt.Errorf("failed to check if law %s is complete: %w", lawKey, err)
	}
	if isComplete {
		// Law is marked as complete - do NOT delete its facts
		return 0, tx.Commit(ctx)
	}

	// Step 1: Find all fact IDs associated with this law_key
	factIDsQuery := `
		SELECT DISTINCT f.id
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id::bigint
		WHERE fs.metadata->>'law_key' = $1
	`
	rows, err := tx.Query(ctx, factIDsQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to find facts for law %s: %w", lawKey, err)
	}

	factIDs := make([]int64, 0)
	for rows.Next() {
		var factID int64
		if err := rows.Scan(&factID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("failed to scan fact ID: %w", err)
		}
		factIDs = append(factIDs, factID)
	}
	rows.Close()

	if len(factIDs) == 0 {
		// No facts to delete
		return 0, tx.Commit(ctx)
	}

	// Step 2: Delete contradictions referencing these facts
	deleteContradictionsQuery := `
		DELETE FROM contradictions
		WHERE fact_a_id = ANY($1) OR fact_b_id = ANY($1)
	`
	_, err = tx.Exec(ctx, deleteContradictionsQuery, factIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to delete contradictions for law %s: %w", lawKey, err)
	}

	// Step 3: Delete fact_mentions (via fact_source_id)
	deleteMentionsQuery := `
		DELETE FROM fact_mentions
		WHERE fact_source_id IN (
			SELECT id FROM fact_sources WHERE metadata->>'law_key' = $1
		)
	`
	_, err = tx.Exec(ctx, deleteMentionsQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to delete fact_mentions for law %s: %w", lawKey, err)
	}

	// Step 4: Delete fact_sources
	deleteSourcesQuery := `DELETE FROM fact_sources WHERE metadata->>'law_key' = $1`
	_, err = tx.Exec(ctx, deleteSourcesQuery, lawKey)
	if err != nil {
		return 0, fmt.Errorf("failed to delete fact_sources for law %s: %w", lawKey, err)
	}

	// Step 5: Delete facts (orphaned by removing fact_sources)
	deleteFactsQuery := `DELETE FROM facts WHERE id = ANY($1)`
	_, err = tx.Exec(ctx, deleteFactsQuery, factIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to delete facts for law %s: %w", lawKey, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return len(factIDs), nil
}

// PersonInfo represents a person with their details.
type PersonInfo struct {
	ID         int64
	Name       string
	EntityType string
}

// GetAllPersonRecords returns all persons as core.PersonRecord, satisfying core.PersonResolver.
func (s *PostgresStorer) GetAllPersonRecords(ctx context.Context) ([]core.PersonRecord, error) {
	persons, err := s.GetAllPersons(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]core.PersonRecord, len(persons))
	for i, p := range persons {
		records[i] = core.PersonRecord{ID: p.ID, Name: p.Name, EntityType: p.EntityType}
	}
	return records, nil
}

// GetAllPersons retrieves all persons with their details (id, name, entity_type).
func (s *PostgresStorer) GetAllPersons(ctx context.Context) ([]PersonInfo, error) {
	query := `SELECT id, name, COALESCE(entity_type, 'person') as entity_type FROM persons ORDER BY name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []PersonInfo
	for rows.Next() {
		var p PersonInfo
		if err := rows.Scan(&p.ID, &p.Name, &p.EntityType); err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, p)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over persons: %w", rows.Err())
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

// AddPersonWithType adds a person with a specific entity_type.
// Valid entity_type values: 'human', 'party', 'company', 'organization'
func (s *PostgresStorer) AddPersonWithType(ctx context.Context, name string, entityType string) (id int64, inserted bool, err error) {
	normalizedName := util.NormalizeString(name)
	if normalizedName == "" {
		return 0, false, fmt.Errorf("cannot add empty person name")
	}

	// Validate entity_type
	validTypes := map[string]bool{"human": true, "party": true, "company": true, "organization": true}
	if !validTypes[entityType] {
		return 0, false, fmt.Errorf("invalid entity_type '%s': must be one of 'human', 'party', 'company', 'organization'", entityType)
	}

	stmt := `
		INSERT INTO persons (name, entity_type) VALUES ($1, $2)
		ON CONFLICT (name) DO NOTHING
		RETURNING id
	`

	err = s.pool.QueryRow(ctx, stmt, normalizedName, entityType).Scan(&id)

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

// GetFactsBySourceType retrieves all facts from a given source type (e.g. "twitter", "news"),
// grouped by person name. Returns a map of person name → facts.
func (s *PostgresStorer) GetFactsBySourceType(ctx context.Context, sourceType string) (map[string][]FactDetail, error) {
	query := `
		SELECT
			p.name AS person_name,
			f.id AS fact_id,
			f.fact_text,
			fs.id AS fact_source_id,
			fs.source_url,
			fs.original_quote
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fs.id = fm.fact_source_id
		JOIN persons p ON fm.person_id = p.id
		WHERE fs.source_type = $1
		ORDER BY p.name, f.id
	`
	rows, err := s.pool.Query(ctx, query, sourceType)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts by source_type %q: %w", sourceType, err)
	}
	defer rows.Close()

	result := make(map[string][]FactDetail)
	for rows.Next() {
		var personName string
		var detail FactDetail
		if err := rows.Scan(&personName, &detail.FactID, &detail.FactText, &detail.FactSourceID, &detail.SourceURL, &detail.OriginalQuote); err != nil {
			return nil, fmt.Errorf("failed to scan fact by source_type: %w", err)
		}
		result[personName] = append(result[personName], detail)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating facts by source_type: %w", rows.Err())
	}

	return result, nil
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
	SourceType string         // Optional source type for observability
	Metadata   map[string]any // Optional plugin metadata
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
	factSourceID, err = s.linkSourceToFact(ctx, tx, input.ExistingFactID, input.SourceURL, input.OriginalQuote, input.PublishedAt, input.SourceType, input.Metadata)
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

// FactWithEmbedding contains a fact's ID, text, and embedding vector.
type FactWithEmbedding struct {
	ID            int64
	Text          string
	Embedding     []float32
	PublishedAt   *time.Time // Earliest publication date from fact_sources
	CanonicalVerb string
}

// GetFactsWithEmbeddingsForPerson returns all active (non-duplicate) facts for a person including their embeddings.
// This is used for batch contradiction detection.
func (s *PostgresStorer) GetFactsWithEmbeddingsForPerson(ctx context.Context, personID int64) ([]FactWithEmbedding, error) {
	query := `
		WITH FactDates AS (
			SELECT fs.fact_id, MIN(fs.published_at) AS earliest_published_at
			FROM fact_sources fs
			GROUP BY fs.fact_id
		)
		SELECT DISTINCT f.id, f.fact_text, f.embedding, fd.earliest_published_at, COALESCE(f.canonical_verb, '') AS canonical_verb
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		LEFT JOIN FactDates fd ON f.id = fd.fact_id
		WHERE fm.person_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY f.id`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	var results []FactWithEmbedding
	for rows.Next() {
		var f FactWithEmbedding
		var emb pgvector.Vector
		if err := rows.Scan(&f.ID, &f.Text, &emb, &f.PublishedAt, &f.CanonicalVerb); err != nil {
			return nil, fmt.Errorf("failed to scan fact for person %d: %w", personID, err)
		}
		f.Embedding = emb.Slice()
		results = append(results, f)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating facts for person %d: %w", personID, rows.Err())
	}

	return results, nil
}

// FindNeighborFactsForPerson searches for facts linked to a specific person that are
// semantically similar to a given embedding, excluding a specific fact ID (usually the new one).
// Returns IDs and text of neighboring facts within the similarity threshold.
type NeighborFactResult struct {
	ID            int64
	Text          string
	Distance      float64    // Include distance for potential future use/logging
	PublishedAt   *time.Time // Earliest publication date from fact_sources
	CanonicalVerb string     // Canonical verb for verb-aware filtering
}

func (s *PostgresStorer) FindNeighborFactsForPerson(ctx context.Context, queryer pgx.Tx, personID int64, embedding []float32, excludeFactID int64) ([]NeighborFactResult, error) {
	timer := metrics.NewQueryTimer("find_neighbor_facts")
	start := time.Now()

	// Query returns ALL active (non-duplicate) facts within the similarity threshold (no LIMIT)
	// This ensures we check all pairs of semantically similar facts
	query := `
        WITH PersonFacts AS (
            -- Select distinct active fact IDs associated with the person
            SELECT DISTINCT fs.fact_id
            FROM fact_mentions fm
            JOIN fact_sources fs ON fm.fact_source_id = fs.id
            WHERE fm.person_id = $1
        ),
        FactDates AS (
            -- Get the earliest published_at date for each fact
            SELECT fs.fact_id, MIN(fs.published_at) AS earliest_published_at
            FROM fact_sources fs
            GROUP BY fs.fact_id
        )
        SELECT
            f.id,
            f.fact_text,
            f.embedding <-> $2 AS distance,
            fd.earliest_published_at,
            COALESCE(f.canonical_verb, '') AS canonical_verb
        FROM facts f
        JOIN PersonFacts pf ON f.id = pf.fact_id
        LEFT JOIN FactDates fd ON f.id = fd.fact_id
        WHERE
            f.id <> $3 -- Exclude the fact itself
        AND
            f.embedding <-> $2 <= $4 -- Similarity threshold (no LIMIT - check ALL similar pairs)
        AND NOT EXISTS (
            SELECT 1 FROM fact_relations fr
            WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
        )
        ORDER BY
            distance`

	rows, err := queryer.Query(ctx, query,
		personID,                      // $1
		pgvector.NewVector(embedding), // $2
		excludeFactID,                 // $3
		embeddingSimilarityThreshold,  // $4
	)
	if err != nil {
		timer.ObserveDuration(err)
		return nil, fmt.Errorf("failed to query neighbor facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	var results []NeighborFactResult
	for rows.Next() {
		var res NeighborFactResult
		if err := rows.Scan(&res.ID, &res.Text, &res.Distance, &res.PublishedAt, &res.CanonicalVerb); err != nil {
			timer.ObserveDuration(err)
			return nil, fmt.Errorf("failed to scan neighbor fact result for person %d: %w", personID, err)
		}
		results = append(results, res)
	}

	err = rows.Err()
	timer.ObserveDuration(err)
	metrics.ObserveVectorSearch(time.Since(start), len(results))

	if err != nil {
		return nil, fmt.Errorf("error iterating neighbor fact results for person %d: %w", personID, err)
	}

	return results, nil
}

// GetExistingContradictionPairs returns a map of existing contradiction pairs for a person.
// The map keys are "factAID-factBID" strings (with smaller ID first) for quick lookup.
func (s *PostgresStorer) GetExistingContradictionPairs(ctx context.Context, personID int64) (map[string]bool, error) {
	query := `SELECT fact_a_id, fact_b_id FROM contradictions WHERE person_id = $1`
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query existing contradictions for person %d: %w", personID, err)
	}
	defer rows.Close()

	pairs := make(map[string]bool)
	for rows.Next() {
		var factAID, factBID int64
		if err := rows.Scan(&factAID, &factBID); err != nil {
			return nil, fmt.Errorf("failed to scan contradiction pair: %w", err)
		}
		// IDs are already normalized (smaller first) in the DB
		key := fmt.Sprintf("%d-%d", factAID, factBID)
		pairs[key] = true
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating contradiction pairs: %w", rows.Err())
	}

	return pairs, nil
}

// PairCheckResult represents the result of a fact pair check.
type PairCheckResult struct {
	FactAID         int64
	FactBID         int64
	IsContradiction bool
}

// GetCheckedPairs returns all previously checked fact pairs for a person.
// Returns map["factAID-factBID"] = true for efficient lookup.
// This includes both contradictory and non-contradictory pairs.
func (s *PostgresStorer) GetCheckedPairs(ctx context.Context, personID int64) (map[string]bool, error) {
	query := `SELECT fact_a_id, fact_b_id FROM fact_pair_checks WHERE person_id = $1`
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query checked pairs for person %d: %w", personID, err)
	}
	defer rows.Close()

	pairs := make(map[string]bool)
	for rows.Next() {
		var factAID, factBID int64
		if err := rows.Scan(&factAID, &factBID); err != nil {
			return nil, fmt.Errorf("failed to scan checked pair: %w", err)
		}
		key := fmt.Sprintf("%d-%d", factAID, factBID)
		pairs[key] = true
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating checked pairs: %w", rows.Err())
	}

	return pairs, nil
}

// RecordPairCheck records that a fact pair was analyzed.
func (s *PostgresStorer) RecordPairCheck(ctx context.Context, personID, factAID, factBID int64, isContradiction bool) error {
	// Ensure consistent ordering
	if factAID > factBID {
		factAID, factBID = factBID, factAID
	}

	query := `
		INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
		SET is_contradiction = EXCLUDED.is_contradiction,
		    checked_at = NOW()`

	_, err := s.pool.Exec(ctx, query, personID, factAID, factBID, isContradiction)
	if err != nil {
		return fmt.Errorf("failed to record pair check for person %d between facts %d and %d: %w", personID, factAID, factBID, err)
	}

	return nil
}

// RecordPairChecksBatch records multiple pair checks efficiently.
func (s *PostgresStorer) RecordPairChecksBatch(ctx context.Context, personID int64, results []PairCheckResult) error {
	if len(results) == 0 {
		return nil
	}

	// Build batch insert query
	query := `
		INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
		SET is_contradiction = EXCLUDED.is_contradiction,
		    checked_at = NOW()`

	batch := &pgx.Batch{}
	for _, result := range results {
		factAID, factBID := result.FactAID, result.FactBID
		// Ensure consistent ordering
		if factAID > factBID {
			factAID, factBID = factBID, factAID
		}
		batch.Queue(query, personID, factAID, factBID, result.IsContradiction)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := 0; i < len(results); i++ {
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("failed to record pair check batch at index %d: %w", i, err)
		}
	}

	return nil
}

// RecordContradictionImmediate records a contradiction and commits immediately.
// Use this when you want each contradiction saved right away (e.g., in CLI commands).
func (s *PostgresStorer) RecordContradictionImmediate(ctx context.Context, personID int64, factAID int64, factBID int64, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.RecordContradiction(ctx, tx, personID, factAID, factBID, reason); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit contradiction: %w", err)
	}

	return nil
}

// RecordContradiction inserts a record indicating that two facts for a person are contradictory.
// Ensures that factAID < factBID to avoid duplicate pairs in different orders.
// Now includes a reason for the contradiction.
// On new contradiction insertion, recomputes the person's honesty score and emits events.
func (s *PostgresStorer) RecordContradiction(ctx context.Context, tx pgx.Tx, personID int64, factAID int64, factBID int64, reason string) error {
	timer := metrics.NewQueryTimer("record_contradiction")

	// Ensure consistent ordering of fact IDs to prevent duplicates like (1,2) and (2,1)
	if factAID > factBID {
		factAID, factBID = factBID, factAID
	}

	query := `
        INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
        SET reason = EXCLUDED.reason, -- Update reason if the pair is reported again
            detected_at = CURRENT_TIMESTAMP
        RETURNING id, (xmax = 0) AS was_inserted`

	var contradictionID int64
	var wasInserted bool
	err := tx.QueryRow(ctx, query, personID, factAID, factBID, reason).Scan(&contradictionID, &wasInserted)
	timer.ObserveDuration(err)

	if err != nil {
		return fmt.Errorf("failed to insert/update contradiction for person %d between facts %d and %d: %w", personID, factAID, factBID, err)
	}

	log.Info().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).Bool("was_new_contradiction", wasInserted).Msg("Recorded/updated contradiction")

	// If this was a new contradiction, recompute honesty score and emit events
	if wasInserted {
		metrics.IncrementContradictionsDetected()
		if err := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, personID, contradictionID); err != nil {
			return fmt.Errorf("failed to recompute honesty score and emit events: %w", err)
		}
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

// GetFactDateByID retrieves the earliest publication date for a fact from its sources.
// Returns nil if no published_at date is available.
func (s *PostgresStorer) GetFactDateByID(ctx context.Context, tx pgx.Tx, factID int64) (*time.Time, error) {
	var publishedAt *time.Time
	query := `SELECT MIN(published_at) FROM fact_sources WHERE fact_id = $1`
	err := tx.QueryRow(ctx, query, factID).Scan(&publishedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // No sources found, return nil date
		}
		return nil, fmt.Errorf("failed to query fact date for ID %d: %w", factID, err)
	}
	return publishedAt, nil
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

// GetPersonAffiliations retrieves all party affiliations for a person that were valid at a specific time
func (s *PostgresStorer) GetPersonAffiliations(ctx context.Context, personID int64, atTime *time.Time) ([]Affiliation, error) {
	query := `
		SELECT
			dst_person_id,
			valid_from,
			valid_to,
			attributes
		FROM person_links
		WHERE src_person_id = $1
		AND link_type = 'affiliation'
		AND ($2::date IS NULL OR (valid_from IS NULL OR valid_from <= $2::date))
		AND ($2::date IS NULL OR (valid_to IS NULL OR valid_to >= $2::date))
	`

	rows, err := s.pool.Query(ctx, query, personID, atTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query affiliations for person %d: %w", personID, err)
	}
	defer rows.Close()

	var affiliations []Affiliation
	for rows.Next() {
		var aff Affiliation
		var validFrom, validTo *time.Time
		var attributes map[string]any

		err := rows.Scan(&aff.PartyID, &validFrom, &validTo, &attributes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan affiliation row for person %d: %w", personID, err)
		}

		aff.StartTime = validFrom
		aff.EndTime = validTo
		aff.Attributes = attributes
		affiliations = append(affiliations, aff)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating affiliation rows for person %d: %w", personID, rows.Err())
	}

	return affiliations, nil
}

// StoreSkippedLaw stores information about a law that was skipped during processing
func (s *PostgresStorer) StoreSkippedLaw(ctx context.Context, lawKey, title string, legislature int, voteDate *time.Time, skipReason, matchedPattern string, aiConfidence *float64) error {
	query := `
		INSERT INTO skipped_laws (law_key, title, legislature, vote_date, skip_reason, matched_pattern, ai_confidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (law_key) DO NOTHING`

	_, err := s.pool.Exec(ctx, query, lawKey, title, legislature, voteDate, skipReason, matchedPattern, aiConfidence)
	if err != nil {
		return fmt.Errorf("failed to store skipped law %s: %w", lawKey, err)
	}
	return nil
}

// HonestyScoreRatioDecay controls the exponential penalty based on contradiction ratio.
const HonestyScoreRatioDecay = 4.0

// HonestyScoreAbsoluteDecay controls the exponential penalty per absolute contradiction.
// Each contradiction contributes an additional fixed penalty regardless of fact count.
const HonestyScoreAbsoluteDecay = 0.02

// ComputeHonestyScore returns exp(-k * contradictions/facts - j * contradictions).
// The hybrid formula penalizes both the ratio and the absolute count of contradictions,
// preventing high fact counts from diluting the impact of many contradictions.
// If factsCount is 0, returns 1.0 (benefit of the doubt).
func ComputeHonestyScore(factsCount, contradictionsCount int) float64 {
	if factsCount == 0 {
		return 1.0
	}
	ratio := float64(contradictionsCount) / float64(factsCount)
	return math.Exp(-HonestyScoreRatioDecay*ratio - HonestyScoreAbsoluteDecay*float64(contradictionsCount))
}

// recomputeHonestyScoreAndEmitEvents recomputes the honesty score for a person and emits relevant events
func (s *PostgresStorer) recomputeHonestyScoreAndEmitEvents(ctx context.Context, tx pgx.Tx, personID int64, contradictionID int64) error {
	// Get counts for honesty score calculation
	factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get facts count for person %d: %w", personID, err)
	}

	contradictionsCount, err := s.getPersonContradictionsCount(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get contradictions count for person %d: %w", personID, err)
	}

	// Calculate honesty score: exp(-k * contradictions/facts)
	honestyScore := ComputeHonestyScore(factsCount, contradictionsCount)

	// Update the honesty score
	if err := s.updatePersonHonestyScore(ctx, tx, personID, honestyScore); err != nil {
		return fmt.Errorf("failed to update honesty score for person %d: %w", personID, err)
	}

	// Record honesty score in Prometheus
	metrics.ObserveHonestyScore(honestyScore)

	// Emit events
	payload := map[string]interface{}{
		"contradiction_id": contradictionID,
		"honesty_score":    honestyScore,
	}

	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := s.insertEvent(ctx, tx, "contradiction_added", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit contradiction_added event: %w", err)
	}

	log.Info().Int64("person_id", personID).Float64("honesty_score", honestyScore).Int("facts_count", factsCount).Int("contradictions_count", contradictionsCount).Msg("Recomputed honesty score and emitted events")
	return nil
}

// getPersonFactsCount returns the number of distinct facts mentioning a person
func (s *PostgresStorer) getPersonFactsCount(ctx context.Context, tx pgx.Tx, personID int64) (int, error) {
	query := `
		SELECT COUNT(DISTINCT f.id)
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1`

	var count int
	err := tx.QueryRow(ctx, query, personID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count facts for person %d: %w", personID, err)
	}
	return count, nil
}

// getPersonContradictionsCount returns the number of contradictions for a person
func (s *PostgresStorer) getPersonContradictionsCount(ctx context.Context, tx pgx.Tx, personID int64) (int, error) {
	query := `SELECT COUNT(*) FROM contradictions WHERE person_id = $1`

	var count int
	err := tx.QueryRow(ctx, query, personID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count contradictions for person %d: %w", personID, err)
	}
	return count, nil
}

// updatePersonHonestyScore updates the honesty score for a person
func (s *PostgresStorer) updatePersonHonestyScore(ctx context.Context, tx pgx.Tx, personID int64, score float64) error {
	query := `UPDATE persons SET honesty_score = $1 WHERE id = $2`

	_, err := tx.Exec(ctx, query, score, personID)
	if err != nil {
		return fmt.Errorf("failed to update honesty score for person %d: %w", personID, err)
	}
	return nil
}

// insertEvent inserts an event into the event_log table
func (s *PostgresStorer) insertEvent(ctx context.Context, tx pgx.Tx, topic string, entityType string, entityID int64, payload map[string]interface{}) error {
	query := `
		INSERT INTO event_log (topic, entity_type, entity_id, payload)
		VALUES ($1, $2, $3, $4)`

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	_, err = tx.Exec(ctx, query, topic, entityType, entityID, payloadJSON)
	if err != nil {
		return fmt.Errorf("failed to insert event: %w", err)
	}
	return nil
}

// PersonSummary represents a summary of a person for the API
type PersonSummary struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	EntityType   *string `json:"entity_type,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	ImageURL     *string `json:"image_url,omitempty"`
	FactCount    int     `json:"fact_count"`
}

// GetPersonsRankedByScore returns persons ranked by fact count (descending), then honesty score (descending)
// entityType can be empty (all), "party", or "human" to filter results
// search performs case-insensitive partial match on name
func (s *PostgresStorer) GetPersonsRankedByScore(ctx context.Context, limit int, offset int, entityType string, search string) ([]PersonSummary, error) {
	query := `
		SELECT
			p.id,
			p.name,
			p.entity_type,
			p.honesty_score,
			p.image_url,
			COUNT(DISTINCT fs.fact_id) as fact_count
		FROM persons p
		LEFT JOIN fact_mentions fm ON p.id = fm.person_id
		LEFT JOIN fact_sources fs ON fm.fact_source_id = fs.id
		WHERE ($3 = '' OR p.entity_type = $3)
		  AND ($4 = '' OR p.name ILIKE '%' || $4 || '%')
		GROUP BY p.id, p.name, p.entity_type, p.honesty_score, p.image_url
		ORDER BY fact_count DESC, p.honesty_score DESC, p.name ASC
		LIMIT $1 OFFSET $2`

	rows, err := s.pool.Query(ctx, query, limit, offset, entityType, search)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []PersonSummary
	for rows.Next() {
		var person PersonSummary
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.ImageURL, &person.FactCount)
		if err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}

// GetPersonByID returns a single person by ID
func (s *PostgresStorer) GetPersonByID(ctx context.Context, personID int64) (*PersonSummary, error) {
	query := `
		SELECT id, name, entity_type, honesty_score, image_url
		FROM persons
		WHERE id = $1`

	var person PersonSummary
	err := s.pool.QueryRow(ctx, query, personID).Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.ImageURL)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query person %d: %w", personID, err)
	}

	return &person, nil
}

// FactWithSource represents a fact with its source information
type FactWithSource struct {
	FactID        int64  `json:"fact_id"`
	FactText      string `json:"fact_text"`
	SourceURL     string `json:"source_url"`
	OriginalQuote string `json:"original_quote"`
}

// PaginatedFacts wraps a page of facts with the total count.
type PaginatedFacts struct {
	Items []FactWithSource `json:"items"`
	Total int              `json:"total"`
}

// PaginatedContradictions wraps a page of contradictions with the total count.
type PaginatedContradictions struct {
	Items []ContradictionWithFacts `json:"items"`
	Total int                     `json:"total"`
}

// GetPersonFacts returns a page of facts for a person together with the total count.
func (s *PostgresStorer) GetPersonFacts(ctx context.Context, personID int64, limit int, offset int) (*PaginatedFacts, error) {
	query := `
		WITH fact_rows AS (
			SELECT DISTINCT f.id, f.fact_text, fs.source_url, fs.original_quote
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
		)
		SELECT id, fact_text, source_url, original_quote,
		       (SELECT COUNT(DISTINCT id) FROM fact_rows) AS total_count
		FROM fact_rows
		ORDER BY id
		LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, query, personID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	result := &PaginatedFacts{Items: make([]FactWithSource, 0)}
	for rows.Next() {
		var fact FactWithSource
		err := rows.Scan(&fact.FactID, &fact.FactText, &fact.SourceURL, &fact.OriginalQuote, &result.Total)
		if err != nil {
			return nil, fmt.Errorf("failed to scan fact: %w", err)
		}
		result.Items = append(result.Items, fact)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating facts: %w", rows.Err())
	}

	// If no rows returned, we still need the total count
	if len(result.Items) == 0 {
		countQuery := `
			SELECT COUNT(DISTINCT f.id)
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1`
		if err := s.pool.QueryRow(ctx, countQuery, personID).Scan(&result.Total); err != nil {
			return nil, fmt.Errorf("failed to count facts for person %d: %w", personID, err)
		}
	}

	return result, nil
}

// ContradictionWithFacts represents a contradiction with both fact texts, sources, and dates
type ContradictionWithFacts struct {
	ID              int64      `json:"id"`
	PersonID        int64      `json:"person_id"`
	FactAID         int64      `json:"fact_a_id"`
	FactBID         int64      `json:"fact_b_id"`
	FactAText       string     `json:"fact_a_text"`
	FactBText       string     `json:"fact_b_text"`
	FactASourceURLs []string   `json:"fact_a_source_urls"`
	FactBSourceURLs []string   `json:"fact_b_source_urls"`
	FactADate       *time.Time `json:"fact_a_date"`
	FactBDate       *time.Time `json:"fact_b_date"`
	Reason          string     `json:"reason"`
	DetectedAt      time.Time  `json:"detected_at"`
}

// GetPersonContradictions returns a page of contradictions for a person together with the total count.
func (s *PostgresStorer) GetPersonContradictions(ctx context.Context, personID int64, limit int, offset int) (*PaginatedContradictions, error) {
	query := `
		WITH contradiction_rows AS (
			SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
				   fa.fact_text AS fact_a_text, fb.fact_text AS fact_b_text,
				   COALESCE(fsa.urls, '{}') AS fact_a_urls, COALESCE(fsb.urls, '{}') AS fact_b_urls,
				   COALESCE(fsa.earliest, fa.created_at) AS fact_a_date,
				   COALESCE(fsb.earliest, fb.created_at) AS fact_b_date,
				   c.reason, c.detected_at
			FROM contradictions c
			JOIN facts fa ON fa.id = c.fact_a_id
			JOIN facts fb ON fb.id = c.fact_b_id
			LEFT JOIN LATERAL (
				SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
				       MIN(published_at) AS earliest
				FROM fact_sources WHERE fact_id = fa.id
			) fsa ON true
			LEFT JOIN LATERAL (
				SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
				       MIN(published_at) AS earliest
				FROM fact_sources WHERE fact_id = fb.id
			) fsb ON true
			WHERE c.person_id = $1
		)
		SELECT id, person_id, fact_a_id, fact_b_id,
		       fact_a_text, fact_b_text,
		       fact_a_urls, fact_b_urls,
		       fact_a_date, fact_b_date,
		       reason, detected_at,
		       (SELECT COUNT(*) FROM contradiction_rows) AS total_count
		FROM contradiction_rows
		ORDER BY detected_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, query, personID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query contradictions for person %d: %w", personID, err)
	}
	defer rows.Close()

	result := &PaginatedContradictions{Items: make([]ContradictionWithFacts, 0)}
	for rows.Next() {
		var c ContradictionWithFacts
		err := rows.Scan(&c.ID, &c.PersonID, &c.FactAID, &c.FactBID,
			&c.FactAText, &c.FactBText,
			&c.FactASourceURLs, &c.FactBSourceURLs,
			&c.FactADate, &c.FactBDate,
			&c.Reason, &c.DetectedAt, &result.Total)
		if err != nil {
			return nil, fmt.Errorf("failed to scan contradiction: %w", err)
		}
		result.Items = append(result.Items, c)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating contradictions: %w", rows.Err())
	}

	// If no rows returned, we still need the total count
	if len(result.Items) == 0 {
		countQuery := `SELECT COUNT(*) FROM contradictions WHERE person_id = $1`
		if err := s.pool.QueryRow(ctx, countQuery, personID).Scan(&result.Total); err != nil {
			return nil, fmt.Errorf("failed to count contradictions for person %d: %w", personID, err)
		}
	}

	return result, nil
}

// DeleteContradictionsForPerson deletes all contradictions for a person and recalculates their honesty score.
// Returns the number of contradictions deleted.
func (s *PostgresStorer) DeleteContradictionsForPerson(ctx context.Context, personID int64) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete all contradictions for this person
	query := `DELETE FROM contradictions WHERE person_id = $1`
	result, err := tx.Exec(ctx, query, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete contradictions for person %d: %w", personID, err)
	}

	deletedCount := int(result.RowsAffected())

	// Reset pair tracking so all pairs are re-analyzed on next detection run
	_, err = tx.Exec(ctx, `DELETE FROM fact_pair_checks WHERE person_id = $1`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to reset pair checks for person %d: %w", personID, err)
	}

	if deletedCount > 0 {
		// Recalculate honesty score (will be 1.0 since no contradictions remain)
		factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
		if err != nil {
			return 0, fmt.Errorf("failed to get facts count for person %d: %w", personID, err)
		}

		honestyScore := ComputeHonestyScore(factsCount, 0)

		if err := s.updatePersonHonestyScore(ctx, tx, personID, honestyScore); err != nil {
			return 0, fmt.Errorf("failed to update honesty score for person %d: %w", personID, err)
		}

		// Emit person_updated event
		payload := map[string]interface{}{
			"honesty_score":          honestyScore,
			"contradictions_deleted": deletedCount,
		}
		if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
			return 0, fmt.Errorf("failed to emit person_updated event: %w", err)
		}

		log.Info().Int64("person_id", personID).Int("deleted", deletedCount).Float64("honesty_score", honestyScore).Msg("Deleted contradictions and updated honesty score")
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return deletedCount, nil
}

// CountFactsForPerson returns the number of facts for a person.
// Facts are linked via fact_mentions -> fact_sources -> facts.
func (s *PostgresStorer) CountFactsForPerson(ctx context.Context, personID int64) (int64, error) {
	query := `
		SELECT COUNT(DISTINCT f.id)
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fs.id = fm.fact_source_id
		WHERE fm.person_id = $1`
	var count int64
	err := s.pool.QueryRow(ctx, query, personID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count facts for person %d: %w", personID, err)
	}
	return count, nil
}

// DeleteFactsForPerson deletes all facts for a person and their associated data.
// Facts are linked via fact_mentions -> fact_sources -> facts.
// Returns the number of facts deleted.
func (s *PostgresStorer) DeleteFactsForPerson(ctx context.Context, personID int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete contradictions for this person first
	_, err = tx.Exec(ctx, `DELETE FROM contradictions WHERE person_id = $1`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete contradictions: %w", err)
	}

	// Clear plugin metadata so plugins will re-process after facts are deleted
	_, err = tx.Exec(ctx, `DELETE FROM news_search_log WHERE person_id = $1`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete news search logs: %w", err)
	}
	_, err = tx.Exec(ctx, `DELETE FROM twitter_fetch_log WHERE handle_id IN (SELECT id FROM twitter_handles WHERE person_id = $1)`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete twitter fetch logs: %w", err)
	}

	// Delete facts linked to this person via fact_mentions
	// The CASCADE on foreign keys will clean up fact_sources and fact_mentions
	result, err := tx.Exec(ctx, `
		DELETE FROM facts
		WHERE id IN (
			SELECT DISTINCT f.id
			FROM facts f
			JOIN fact_sources fs ON f.id = fs.fact_id
			JOIN fact_mentions fm ON fs.id = fm.fact_source_id
			WHERE fm.person_id = $1
		)`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete facts for person %d: %w", personID, err)
	}

	deletedCount := result.RowsAffected()

	// Reset honesty score to 1.0
	if err := s.updatePersonHonestyScore(ctx, tx, personID, 1.0); err != nil {
		return 0, fmt.Errorf("failed to reset honesty score: %w", err)
	}

	// Emit person_updated event
	payload := map[string]interface{}{
		"honesty_score": 1.0,
		"facts_deleted": deletedCount,
	}
	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return 0, fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Info().Int64("person_id", personID).Int64("deleted", deletedCount).Msg("Deleted facts for person")
	return deletedCount, nil
}

// GetFactPersons returns persons who have a fact related to the given fact.
// If the fact_text starts with "collectively ", it removes the prefix to find individual-level facts.
// This is useful for finding persons associated with individual facts when given a collective fact.
func (s *PostgresStorer) GetFactPersons(ctx context.Context, factID int64, limit int, offset int) ([]PersonSummary, error) {
	query := `
		WITH source_fact AS (
			SELECT id, fact_text
			FROM facts
			WHERE id = $1
		),
		transformed_fact_text AS (
			SELECT
				id,
				CASE
					WHEN fact_text LIKE 'collectively %' THEN substring(fact_text from 14)
					ELSE fact_text
				END as transformed_text
			FROM source_fact
		),
		target_fact AS (
			SELECT f.id
			FROM facts f, transformed_fact_text tft
			WHERE f.fact_text = tft.transformed_text
			LIMIT 1
		)
		SELECT DISTINCT
			p.id, p.name, p.entity_type, p.honesty_score, p.image_url
		FROM target_fact tf
		JOIN fact_sources fs ON fs.fact_id = tf.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		JOIN persons p ON p.id = fm.person_id
		ORDER BY p.name ASC
		LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, query, factID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons for fact %d: %w", factID, err)
	}
	defer rows.Close()

	persons := make([]PersonSummary, 0)
	for rows.Next() {
		var person PersonSummary
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.ImageURL)
		if err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}

// --- News Search Logging ---

// LogNewsSearch records a news search attempt for prioritization.
// This helps track which persons have news coverage and which don't.
func (s *PostgresStorer) LogNewsSearch(ctx context.Context, personID int64, query string, resultCount int, factsExtracted int) error {
	insertQuery := `
		INSERT INTO news_search_log (person_id, query, result_count, facts_extracted)
		VALUES ($1, $2, $3, $4)`

	_, err := s.pool.Exec(ctx, insertQuery, personID, query, resultCount, factsExtracted)
	if err != nil {
		return fmt.Errorf("failed to log news search for person %d: %w", personID, err)
	}

	log.Debug().
		Int64("person_id", personID).
		Str("query", query).
		Int("result_count", resultCount).
		Int("facts_extracted", factsExtracted).
		Msg("Logged news search")

	return nil
}

// ShouldSearchNews determines if we should search news for a person based on history.
// Returns true if:
// - No previous search exists (first time)
// - Previous search found results (facts_extracted > 0)
// - No search in the last lookbackDays
func (s *PostgresStorer) ShouldSearchNews(ctx context.Context, personID int64, lookbackDays int) (bool, error) {
	query := `
		SELECT
			COUNT(*) as search_count,
			COALESCE(MAX(facts_extracted), 0) as max_facts,
			COALESCE(MAX(searched_at), '1970-01-01'::timestamptz) as last_search
		FROM news_search_log
		WHERE person_id = $1
		AND searched_at > NOW() - $2::interval`

	var searchCount int
	var maxFacts int
	var lastSearch time.Time
	interval := fmt.Sprintf("%d days", lookbackDays)

	err := s.pool.QueryRow(ctx, query, personID, interval).Scan(&searchCount, &maxFacts, &lastSearch)
	if err != nil {
		return false, fmt.Errorf("failed to check news search history for person %d: %w", personID, err)
	}

	// No recent search: should search
	if searchCount == 0 {
		return true, nil
	}

	// Previous searches found results: should search again
	if maxFacts > 0 {
		return true, nil
	}

	// Previous searches found no results: skip
	return false, nil
}

// GetNewsSearchHistory returns the news search history for a person.
func (s *PostgresStorer) GetNewsSearchHistory(ctx context.Context, personID int64, limit int) ([]NewsSearchLogEntry, error) {
	query := `
		SELECT id, person_id, searched_at, result_count, facts_extracted, query
		FROM news_search_log
		WHERE person_id = $1
		ORDER BY searched_at DESC
		LIMIT $2`

	rows, err := s.pool.Query(ctx, query, personID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get news search history for person %d: %w", personID, err)
	}
	defer rows.Close()

	entries := make([]NewsSearchLogEntry, 0)
	for rows.Next() {
		var entry NewsSearchLogEntry
		var queryStr *string
		err := rows.Scan(&entry.ID, &entry.PersonID, &entry.SearchedAt, &entry.ResultCount, &entry.FactsExtracted, &queryStr)
		if err != nil {
			return nil, fmt.Errorf("failed to scan news search log entry: %w", err)
		}
		if queryStr != nil {
			entry.Query = *queryStr
		}
		entries = append(entries, entry)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating news search log: %w", rows.Err())
	}

	return entries, nil
}

// NewsSearchLogEntry represents a single entry in the news search log.
type NewsSearchLogEntry struct {
	ID             int64
	PersonID       int64
	SearchedAt     time.Time
	ResultCount    int
	FactsExtracted int
	Query          string
}

// --- News Queue Management ---

// QueueEntry represents a person in the news queue.
type QueueEntry struct {
	ID         int64
	PersonID   int64
	Name       string
	EntityType string
	AddedAt    time.Time
}

// AddToSearchQueue adds a person to the search queue.
// Returns an error if the person doesn't exist or if the database operation fails.
// If the person is already in the queue, this is a no-op (returns nil).
func (s *PostgresStorer) AddToSearchQueue(ctx context.Context, personID int64) error {
	stmt := `
		INSERT INTO search_queue (person_id)
		VALUES ($1)
		ON CONFLICT (person_id) DO NOTHING`

	result, err := s.pool.Exec(ctx, stmt, personID)
	if err != nil {
		return fmt.Errorf("failed to add person %d to search queue: %w", personID, err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		log.Debug().Int64("person_id", personID).Msg("Person already in search queue")
	} else {
		log.Info().Int64("person_id", personID).Msg("Added person to search queue")
	}

	return nil
}

// RemoveFromSearchQueue removes a person from the search queue.
// Returns an error if the person is not in the queue.
func (s *PostgresStorer) RemoveFromSearchQueue(ctx context.Context, personID int64) error {
	stmt := `DELETE FROM search_queue WHERE person_id = $1`

	result, err := s.pool.Exec(ctx, stmt, personID)
	if err != nil {
		return fmt.Errorf("failed to remove person %d from search queue: %w", personID, err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("person %d not found in search queue", personID)
	}

	log.Info().Int64("person_id", personID).Msg("Removed person from search queue")
	return nil
}

// GetSearchQueue retrieves all persons in the search queue.
// Results are ordered by person_id ASC (oldest persons first).
func (s *PostgresStorer) GetSearchQueue(ctx context.Context) ([]QueueEntry, error) {
	query := `
		SELECT q.id, q.person_id, p.name, COALESCE(p.entity_type, 'human'), q.added_at
		FROM search_queue q
		JOIN persons p ON q.person_id = p.id
		ORDER BY q.person_id ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get search queue: %w", err)
	}
	defer rows.Close()

	queue := make([]QueueEntry, 0)
	for rows.Next() {
		var entry QueueEntry
		err := rows.Scan(&entry.ID, &entry.PersonID, &entry.Name, &entry.EntityType, &entry.AddedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan queue entry: %w", err)
		}
		queue = append(queue, entry)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating search queue: %w", rows.Err())
	}

	return queue, nil
}

// IsInSearchQueue checks if a person is in the search queue.
func (s *PostgresStorer) IsInSearchQueue(ctx context.Context, personID int64) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM search_queue WHERE person_id = $1)`

	var exists bool
	err := s.pool.QueryRow(ctx, query, personID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if person %d is in queue: %w", personID, err)
	}

	return exists, nil
}

// --- Deduplication Methods ---

// FactForDedup represents a fact with fields needed for deduplication.
type FactForDedup struct {
	ID                 int64
	NormalizedFactText string
	CanonicalVerb      string
	EmbeddingText      string
	OriginalQuote      string
	Embedding          []float32
	IsCanonical        bool
	CanonicalFactID    *int64
}

// GetFactsForDeduplication retrieves all facts for a person with their embeddings.
// Includes whether each fact is a duplicate (specific side of a 'duplicate' relation).
func (s *PostgresStorer) GetFactsForDeduplication(ctx context.Context, personID int64) ([]FactForDedup, error) {
	query := `
		SELECT DISTINCT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
			   COALESCE(f.canonical_verb, ''),
			   COALESCE(fs.original_quote, ''), f.embedding,
			   NOT EXISTS (
			       SELECT 1 FROM fact_relations fr
			       WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			   ) AS is_active
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		ORDER BY f.id`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for deduplication: %w", err)
	}
	defer rows.Close()

	var results []FactForDedup
	for rows.Next() {
		var f FactForDedup
		var emb pgvector.Vector
		var isActive bool
		err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText,
			&f.CanonicalVerb, &f.OriginalQuote, &emb, &isActive)
		if err != nil {
			return nil, fmt.Errorf("failed to scan fact for deduplication: %w", err)
		}
		f.Embedding = emb.Slice()
		f.IsCanonical = isActive // Reuse field: true means active (not a duplicate)
		f.CanonicalFactID = nil  // No longer used
		results = append(results, f)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating facts for deduplication: %w", rows.Err())
	}

	return results, nil
}

// SimilarFactForDedup represents a similar fact found during deduplication search.
type SimilarFactForDedup struct {
	ID         int64
	Text       string
	Distance   float64
	Similarity float64
}

// FindSimilarFactsForDedup finds active (non-duplicate) facts similar to a given embedding.
// Returns facts within the distance threshold, excluding the specified fact ID.
func (s *PostgresStorer) FindSimilarFactsForDedup(ctx context.Context, personIDs []int64, embedding []float32, excludeFactID int64, distanceThreshold float64) ([]SimilarFactForDedup, error) {
	query := `
		WITH PersonFacts AS (
			SELECT DISTINCT fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = ANY($1)
		)
		SELECT f.id, f.fact_text, f.embedding <=> $2 AS distance
		FROM facts f
		JOIN PersonFacts pf ON f.id = pf.fact_id
		WHERE f.id <> $3
		  AND f.embedding <=> $2 <= $4
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY distance
		LIMIT 20`

	rows, err := s.pool.Query(ctx, query,
		personIDs,
		pgvector.NewVector(embedding),
		excludeFactID,
		distanceThreshold,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to find similar facts for dedup: %w", err)
	}
	defer rows.Close()

	var results []SimilarFactForDedup
	for rows.Next() {
		var sf SimilarFactForDedup
		if err := rows.Scan(&sf.ID, &sf.Text, &sf.Distance); err != nil {
			return nil, fmt.Errorf("failed to scan similar fact: %w", err)
		}
		sf.Similarity = 1.0 - sf.Distance
		results = append(results, sf)
	}

	return results, rows.Err()
}

// MarkFactAsDuplicate marks a fact as a duplicate of a canonical (general) fact
// by inserting a 'duplicate' relation into fact_relations.
func (s *PostgresStorer) MarkFactAsDuplicate(ctx context.Context, duplicateID int64, canonicalID int64) error {
	query := `
		INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		VALUES ($1, $2, 'duplicate', 'Marked as duplicate')
		ON CONFLICT (general_fact_id, specific_fact_id) DO UPDATE
		SET relation_type = 'duplicate',
		    reason = EXCLUDED.reason`

	_, err := s.pool.Exec(ctx, query, canonicalID, duplicateID)
	if err != nil {
		return fmt.Errorf("failed to mark fact %d as duplicate of %d: %w", duplicateID, canonicalID, err)
	}

	log.Info().
		Int64("duplicate_id", duplicateID).
		Int64("canonical_id", canonicalID).
		Msg("Marked fact as duplicate")

	return nil
}

// GetFactByID retrieves a single fact by its ID for deduplication.
func (s *PostgresStorer) GetFactByID(ctx context.Context, factID int64) (*FactForDedup, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
			   COALESCE(f.canonical_verb, ''), f.embedding,
			   NOT EXISTS (
			       SELECT 1 FROM fact_relations fr
			       WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			   ) AS is_active
		FROM facts f
		WHERE f.id = $1`

	var f FactForDedup
	var emb pgvector.Vector
	var isActive bool
	err := s.pool.QueryRow(ctx, query, factID).Scan(
		&f.ID, &f.NormalizedFactText, &f.EmbeddingText, &f.CanonicalVerb, &emb, &isActive)

	if err != nil {
		return nil, fmt.Errorf("failed to get fact %d: %w", factID, err)
	}

	f.Embedding = emb.Slice()
	f.IsCanonical = isActive // true = active (not a duplicate)
	f.CanonicalFactID = nil
	return &f, nil
}

// GetDuplicateFactsCount returns the count of duplicate facts (specific side of 'duplicate' relations).
func (s *PostgresStorer) GetDuplicateFactsCount(ctx context.Context) (int, error) {
	query := `SELECT COUNT(DISTINCT specific_fact_id) FROM fact_relations WHERE relation_type = 'duplicate'`

	var count int
	err := s.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count duplicate facts: %w", err)
	}

	return count, nil
}

// GetDuplicateFactsByCanonicalID returns all facts marked as duplicates of a given fact
// via the fact_relations table.
func (s *PostgresStorer) GetDuplicateFactsByCanonicalID(ctx context.Context, canonicalID int64) ([]FactForDedup, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), f.embedding
		FROM facts f
		JOIN fact_relations fr ON fr.specific_fact_id = f.id
		WHERE fr.general_fact_id = $1 AND fr.relation_type = 'duplicate'`

	rows, err := s.pool.Query(ctx, query, canonicalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get duplicates for canonical %d: %w", canonicalID, err)
	}
	defer rows.Close()

	var results []FactForDedup
	for rows.Next() {
		var f FactForDedup
		var emb pgvector.Vector
		err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText, &emb)
		if err != nil {
			return nil, fmt.Errorf("failed to scan duplicate fact: %w", err)
		}
		f.Embedding = emb.Slice()
		f.IsCanonical = false
		results = append(results, f)
	}

	return results, rows.Err()
}

// UpdateFactText updates a fact's text and embedding_text fields.
// This is used when improving/normalizing facts during deduplication.
func (s *PostgresStorer) UpdateFactText(ctx context.Context, factID int64, normalizedFact string, embeddingText string) error {
	query := `
		UPDATE facts
		SET fact_text = $1, embedding_text = $2
		WHERE id = $3`

	result, err := s.pool.Exec(ctx, query, normalizedFact, embeddingText, factID)
	if err != nil {
		return fmt.Errorf("failed to update fact %d text: %w", factID, err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("fact %d not found", factID)
	}

	log.Info().
		Int64("fact_id", factID).
		Str("new_text", normalizedFact[:minInt(80, len(normalizedFact))]).
		Msg("Updated fact text")

	return nil
}

// UpdateFactEmbedding updates a fact's embedding vector.
// This should be called after UpdateFactText with a newly generated embedding.
func (s *PostgresStorer) UpdateFactEmbedding(ctx context.Context, factID int64, embedding []float32) error {
	query := `
		UPDATE facts
		SET embedding = $1
		WHERE id = $2`

	result, err := s.pool.Exec(ctx, query, pgvector.NewVector(embedding), factID)
	if err != nil {
		return fmt.Errorf("failed to update fact %d embedding: %w", factID, err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("fact %d not found", factID)
	}

	log.Debug().Int64("fact_id", factID).Msg("Updated fact embedding")

	return nil
}

// DeleteFact deletes a fact and its associated sources/mentions.
// This is used when a fact is determined to be invalid (not a real fact about the person).
func (s *PostgresStorer) DeleteFact(ctx context.Context, factID int64) error {
	// Delete in order: mentions -> sources -> fact
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete fact_mentions for all sources of this fact
	_, err = tx.Exec(ctx, `
		DELETE FROM fact_mentions
		WHERE fact_source_id IN (SELECT id FROM fact_sources WHERE fact_id = $1)`, factID)
	if err != nil {
		return fmt.Errorf("failed to delete fact mentions: %w", err)
	}

	// Delete fact_sources
	_, err = tx.Exec(ctx, `DELETE FROM fact_sources WHERE fact_id = $1`, factID)
	if err != nil {
		return fmt.Errorf("failed to delete fact sources: %w", err)
	}

	// Delete the fact itself
	result, err := tx.Exec(ctx, `DELETE FROM facts WHERE id = $1`, factID)
	if err != nil {
		return fmt.Errorf("failed to delete fact: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("fact %d not found", factID)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit deletion: %w", err)
	}

	log.Info().Int64("fact_id", factID).Msg("Deleted invalid fact")

	return nil
}

// --- Fact Relations Methods ---

// InsertFactRelation inserts a single fact relation. On conflict, updates reason.
func (s *PostgresStorer) InsertFactRelation(ctx context.Context, input core.FactRelationInput) error {
	query := `
		INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (general_fact_id, specific_fact_id) DO UPDATE
		SET relation_type = EXCLUDED.relation_type,
		    reason = EXCLUDED.reason`

	_, err := s.pool.Exec(ctx, query, input.GeneralFactID, input.SpecificFactID, input.RelationType, input.Reason)
	if err != nil {
		return fmt.Errorf("failed to insert fact relation (%d -> %d, %s): %w",
			input.GeneralFactID, input.SpecificFactID, input.RelationType, err)
	}

	log.Debug().
		Int64("general", input.GeneralFactID).
		Int64("specific", input.SpecificFactID).
		Str("type", input.RelationType).
		Msg("Inserted fact relation")

	return nil
}

// InsertFactRelationsBatch inserts multiple fact relations efficiently.
func (s *PostgresStorer) InsertFactRelationsBatch(ctx context.Context, inputs []core.FactRelationInput) error {
	if len(inputs) == 0 {
		return nil
	}

	query := `
		INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (general_fact_id, specific_fact_id) DO UPDATE
		SET relation_type = EXCLUDED.relation_type,
		    reason = EXCLUDED.reason`

	batch := &pgx.Batch{}
	for _, input := range inputs {
		batch.Queue(query, input.GeneralFactID, input.SpecificFactID, input.RelationType, input.Reason)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := 0; i < len(inputs); i++ {
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("failed to insert fact relation batch at index %d: %w", i, err)
		}
	}

	log.Debug().Int("count", len(inputs)).Msg("Inserted fact relations batch")
	return nil
}

// GetMostGeneralAncestor walks UP the implication chain from a given factID
// using a recursive CTE with depth limit 10.
// Returns the most general ancestor fact ID (or the input factID if no implication chain exists).
func (s *PostgresStorer) GetMostGeneralAncestor(ctx context.Context, factID int64) (int64, error) {
	query := `
		WITH RECURSIVE ancestors AS (
			SELECT $1::bigint AS fact_id, 0 AS depth
			UNION ALL
			SELECT fr.general_fact_id, a.depth + 1
			FROM fact_relations fr
			JOIN ancestors a ON fr.specific_fact_id = a.fact_id
			WHERE fr.relation_type = 'implication' AND a.depth < 10
		)
		SELECT fact_id FROM ancestors ORDER BY depth DESC LIMIT 1`

	var ancestorID int64
	err := s.pool.QueryRow(ctx, query, factID).Scan(&ancestorID)
	if err != nil {
		return 0, fmt.Errorf("failed to get most general ancestor for fact %d: %w", factID, err)
	}

	return ancestorID, nil
}

// IsDuplicate checks if a fact is the specific side of a 'duplicate' relation.
func (s *PostgresStorer) IsDuplicate(ctx context.Context, factID int64) (bool, error) {
	query := `SELECT EXISTS(
		SELECT 1 FROM fact_relations
		WHERE specific_fact_id = $1 AND relation_type = 'duplicate'
	)`

	var isDup bool
	err := s.pool.QueryRow(ctx, query, factID).Scan(&isDup)
	if err != nil {
		return false, fmt.Errorf("failed to check if fact %d is duplicate: %w", factID, err)
	}

	return isDup, nil
}

// IsAncestorOf checks if ancestorID is an ancestor of descendantID in the implication chain.
// Used for cycle prevention before inserting implication relations.
func (s *PostgresStorer) IsAncestorOf(ctx context.Context, ancestorID int64, descendantID int64) (bool, error) {
	query := `
		WITH RECURSIVE ancestors AS (
			SELECT $2::bigint AS fact_id, 0 AS depth
			UNION ALL
			SELECT fr.general_fact_id, a.depth + 1
			FROM fact_relations fr
			JOIN ancestors a ON fr.specific_fact_id = a.fact_id
			WHERE fr.relation_type = 'implication' AND a.depth < 10
		)
		SELECT EXISTS(SELECT 1 FROM ancestors WHERE fact_id = $1 AND depth > 0)`

	var isAnc bool
	err := s.pool.QueryRow(ctx, query, ancestorID, descendantID).Scan(&isAnc)
	if err != nil {
		return false, fmt.Errorf("failed to check ancestry (%d -> %d): %w", ancestorID, descendantID, err)
	}

	return isAnc, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Twitter Handle Management ---

// TwitterHandle represents a Twitter handle linked to a person.
type TwitterHandle struct {
	ID            int64
	PersonID      int64
	PersonName    string
	Handle        string
	LastFetchedAt *time.Time
	Active        bool
}

// AddTwitterHandle links a Twitter handle to a person.
// If the handle already exists for this person, this is a no-op.
func (s *PostgresStorer) AddTwitterHandle(ctx context.Context, personID int64, handle string) error {
	stmt := `
		INSERT INTO twitter_handles (person_id, handle)
		VALUES ($1, $2)
		ON CONFLICT (person_id, handle) DO NOTHING`

	result, err := s.pool.Exec(ctx, stmt, personID, handle)
	if err != nil {
		return fmt.Errorf("failed to add twitter handle @%s for person %d: %w", handle, personID, err)
	}

	if result.RowsAffected() == 0 {
		log.Debug().Int64("person_id", personID).Str("handle", handle).Msg("Twitter handle already exists")
	} else {
		log.Info().Int64("person_id", personID).Str("handle", handle).Msg("Added twitter handle")
	}

	return nil
}

// RemoveTwitterHandle removes a Twitter handle from a person.
func (s *PostgresStorer) RemoveTwitterHandle(ctx context.Context, personID int64, handle string) error {
	stmt := `DELETE FROM twitter_handles WHERE person_id = $1 AND handle = $2`

	result, err := s.pool.Exec(ctx, stmt, personID, handle)
	if err != nil {
		return fmt.Errorf("failed to remove twitter handle @%s for person %d: %w", handle, personID, err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("twitter handle @%s not found for person %d", handle, personID)
	}

	log.Info().Int64("person_id", personID).Str("handle", handle).Msg("Removed twitter handle")
	return nil
}

// GetActiveTwitterHandles returns all active Twitter handles with their person names.
func (s *PostgresStorer) GetActiveTwitterHandles(ctx context.Context) ([]TwitterHandle, error) {
	query := `
		SELECT th.id, th.person_id, p.name, th.handle, th.last_fetched_at, th.active
		FROM twitter_handles th
		JOIN persons p ON th.person_id = p.id
		WHERE th.active = true
		ORDER BY th.person_id ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active twitter handles: %w", err)
	}
	defer rows.Close()

	var handles []TwitterHandle
	for rows.Next() {
		var h TwitterHandle
		if err := rows.Scan(&h.ID, &h.PersonID, &h.PersonName, &h.Handle, &h.LastFetchedAt, &h.Active); err != nil {
			return nil, fmt.Errorf("failed to scan twitter handle: %w", err)
		}
		handles = append(handles, h)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating twitter handles: %w", rows.Err())
	}

	return handles, nil
}

// GetTwitterHandlesByPersonID returns all Twitter handles for a specific person.
func (s *PostgresStorer) GetTwitterHandlesByPersonID(ctx context.Context, personID int64) ([]TwitterHandle, error) {
	query := `
		SELECT th.id, th.person_id, p.name, th.handle, th.last_fetched_at, th.active
		FROM twitter_handles th
		JOIN persons p ON th.person_id = p.id
		WHERE th.person_id = $1
		ORDER BY th.handle ASC`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to get twitter handles for person %d: %w", personID, err)
	}
	defer rows.Close()

	var handles []TwitterHandle
	for rows.Next() {
		var h TwitterHandle
		if err := rows.Scan(&h.ID, &h.PersonID, &h.PersonName, &h.Handle, &h.LastFetchedAt, &h.Active); err != nil {
			return nil, fmt.Errorf("failed to scan twitter handle: %w", err)
		}
		handles = append(handles, h)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating twitter handles: %w", rows.Err())
	}

	return handles, nil
}

// UpdateTwitterHandleLastFetched updates the last_fetched_at timestamp for a handle.
func (s *PostgresStorer) UpdateTwitterHandleLastFetched(ctx context.Context, handleID int64, t time.Time) error {
	stmt := `UPDATE twitter_handles SET last_fetched_at = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, stmt, handleID, t)
	if err != nil {
		return fmt.Errorf("failed to update last_fetched_at for handle %d: %w", handleID, err)
	}
	return nil
}

// LogTwitterFetch records that a date range was fetched for a handle.
func (s *PostgresStorer) LogTwitterFetch(ctx context.Context, handleID int64, dateFrom, dateTo time.Time, tweetsFetched int) error {
	stmt := `
		INSERT INTO twitter_fetch_log (handle_id, date_from, date_to, tweets_fetched)
		VALUES ($1, $2, $3, $4)`

	_, err := s.pool.Exec(ctx, stmt, handleID, dateFrom, dateTo, tweetsFetched)
	if err != nil {
		return fmt.Errorf("failed to log twitter fetch for handle %d: %w", handleID, err)
	}
	return nil
}

// IsTwitterRangeFetched checks if a date range has already been fetched for a handle.
// Returns true if an existing log entry fully covers the requested range.
func (s *PostgresStorer) IsTwitterRangeFetched(ctx context.Context, handleID int64, dateFrom, dateTo time.Time) (bool, error) {
	query := `SELECT EXISTS(
		SELECT 1 FROM twitter_fetch_log
		WHERE handle_id = $1 AND date_from <= $2 AND date_to >= $3
	)`

	var exists bool
	err := s.pool.QueryRow(ctx, query, handleID, dateFrom, dateTo).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check twitter fetch log for handle %d: %w", handleID, err)
	}

	return exists, nil
}

// SetPersonImageURL sets the image_url for a person.
func (s *PostgresStorer) SetPersonImageURL(ctx context.Context, personID int64, imageURL string) error {
	_, err := s.pool.Exec(ctx, "UPDATE persons SET image_url = $1 WHERE id = $2", imageURL, personID)
	if err != nil {
		return fmt.Errorf("failed to set image URL for person %d: %w", personID, err)
	}
	return nil
}

// GetPersonsWithoutImage returns all persons that don't have an image_url set.
func (s *PostgresStorer) GetPersonsWithoutImage(ctx context.Context) ([]PersonSummary, error) {
	query := `
		SELECT id, name, entity_type, honesty_score, image_url
		FROM persons
		WHERE image_url IS NULL
		ORDER BY name ASC`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons without image: %w", err)
	}
	defer rows.Close()

	var persons []PersonSummary
	for rows.Next() {
		var person PersonSummary
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.ImageURL)
		if err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}

// JobMetric represents a single metric data point from a CLI job.
type JobMetric struct {
	JobType string
	Target  string
	Metric  string
	Value   float64
}

// RecordJobMetrics inserts multiple job metrics in a single batch.
func (s *PostgresStorer) RecordJobMetrics(ctx context.Context, metrics []JobMetric) error {
	if len(metrics) == 0 {
		return nil
	}

	query := "INSERT INTO job_metrics (job_type, target, metric, value) VALUES "
	args := make([]interface{}, 0, len(metrics)*4)
	for i, m := range metrics {
		if i > 0 {
			query += ", "
		}
		base := i * 4
		query += fmt.Sprintf("($%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4)
		args = append(args, m.JobType, m.Target, m.Metric, m.Value)
	}

	_, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("record job metrics: %w", err)
	}
	return nil
}

// ScoreUpdate holds old and new honesty scores for a person, used by RecomputeAllHonestyScores.
type ScoreUpdate struct {
	PersonID      int64
	PersonName    string
	OldScore      float64
	NewScore      float64
	Facts         int
	Contradictions int
}

// RecomputeAllHonestyScores recalculates honesty scores for every person
// using ComputeHonestyScore. If dryRun is true, scores are not written.
func (s *PostgresStorer) RecomputeAllHonestyScores(ctx context.Context, dryRun bool) ([]ScoreUpdate, error) {
	query := `
		SELECT p.id, p.name, p.honesty_score,
		       COALESCE(fc.cnt, 0) AS facts_count,
		       COALESCE(cc.cnt, 0) AS contradictions_count
		FROM persons p
		LEFT JOIN (
			SELECT fm.person_id, COUNT(DISTINCT f.id) AS cnt
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			GROUP BY fm.person_id
		) fc ON fc.person_id = p.id
		LEFT JOIN (
			SELECT person_id, COUNT(*) AS cnt
			FROM contradictions
			GROUP BY person_id
		) cc ON cc.person_id = p.id
		ORDER BY p.id`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons for score recompute: %w", err)
	}
	defer rows.Close()

	var updates []ScoreUpdate
	for rows.Next() {
		var u ScoreUpdate
		if err := rows.Scan(&u.PersonID, &u.PersonName, &u.OldScore, &u.Facts, &u.Contradictions); err != nil {
			return nil, fmt.Errorf("failed to scan person row: %w", err)
		}
		u.NewScore = ComputeHonestyScore(u.Facts, u.Contradictions)
		updates = append(updates, u)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	if dryRun {
		return updates, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, u := range updates {
		if _, err := tx.Exec(ctx, `UPDATE persons SET honesty_score = $1 WHERE id = $2`, u.NewScore, u.PersonID); err != nil {
			return nil, fmt.Errorf("failed to update score for person %d: %w", u.PersonID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit score updates: %w", err)
	}

	return updates, nil
}
