package storage

import (
	"context"
	"encoding/json"
	"errors"
	// "digital-mirror/backend" // Removed unused import
	// Assuming backend.Fact is no longer needed here directly
	"digital-mirror/backend/core"
	"digital-mirror/backend/entityvalidator"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util" // Import the new util package
	"fmt"
	"math"
	"os"
	"strings"
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
	// Lang is the BCP-47 source language of the normalized fact ("fr", "en", ...).
	// Required: all new inserts MUST provide this — extractors derive it from the LLM output.
	Lang string
	// NormalizedFactText is the core, normalized factual statement in NATIVE language
	// (with person names for display).
	NormalizedFactText string
	// NormalizedFactTextEn is the English translation of NormalizedFactText.
	// Empty when source is already English; falls back to NULL in fact_text_en column.
	NormalizedFactTextEn string
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
	// validator is consulted before every NEW person INSERT (existing persons
	// are never re-validated). nil ⇒ validation disabled. See entity_validator.go.
	validator EntityValidator
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
	config.MaxConnLifetime = 30 * time.Minute   // Recycle connections periodically
	config.MaxConnIdleTime = 5 * time.Minute    // Close idle connections before server times them out
	config.HealthCheckPeriod = 30 * time.Second // Check connection health periodically
	config.MaxConns = 10                        // Reasonable pool size
	config.MinConns = 2                         // Keep some connections warm

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

	// Backstop for the "every fact has a stance" invariant — code-enforced at
	// insertion, but a periodic gauge surfaces any historical drift.
	metrics.StartStanceInvariantCollector(ctx, pool, 5*time.Minute)

	s := &PostgresStorer{
		pool: pool,
		// embedder: embedder, // REMOVED
		// contradictor: contradictor, // REMOVED
	}
	// Attach the default entity-validator chain (WikiFilter + LLMFilter if env
	// permits). This is the single seam every new-person INSERT in the codebase
	// flows through, so the daily pipeline and CLI both get the same policy.
	// Tests / specialized callers may override via SetEntityValidator(nil) or
	// a custom implementation.
	s.validator = entityvalidator.BuildFromEnv()
	return s, nil
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

// personIDByColumn returns the person.id matching whereSQL = val. whereSQL is a
// fixed SQL fragment (never user input); val is bound as $1. Backs the
// GetPersonIDBy* lookups below.
func (s *PostgresStorer) personIDByColumn(ctx context.Context, label, whereSQL, val string) (int64, error) {
	var id int64
	query := fmt.Sprintf(`SELECT id FROM persons WHERE %s = $1 LIMIT 1`, whereSQL)
	if err := s.pool.QueryRow(ctx, query, val).Scan(&id); err != nil {
		return 0, fmt.Errorf("failed to find person by %s %s: %w", label, val, err)
	}
	return id, nil
}

// GetPersonIDByAssembleeID returns the person.id associated with the given assemblee_id in persons.attributes.
func (s *PostgresStorer) GetPersonIDByAssembleeID(ctx context.Context, assembleeID string) (int64, error) {
	return s.personIDByColumn(ctx, "assemblee_id", "attributes->>'assemblee_id'", assembleeID)
}

// GetPersonIDByOrgRef returns the person.id associated with the given orgRef stored in persons.attributes.
func (s *PostgresStorer) GetPersonIDByOrgRef(ctx context.Context, orgRef string) (int64, error) {
	return s.personIDByColumn(ctx, "orgRef", "attributes->>'organeRef'", orgRef)
}

// GetPersonIDByName returns the person.id associated with the given name.
func (s *PostgresStorer) GetPersonIDByName(ctx context.Context, name string) (int64, error) {
	return s.personIDByColumn(ctx, "name", "name", name)
}

// GetSourceURLsForPerson returns every distinct source_url found in fact_sources
// for facts mentioning the given person. Used by `dm scrape urls --from-person`
// to re-scrape an existing corpus without re-running paid web search.
func (s *PostgresStorer) GetSourceURLsForPerson(ctx context.Context, personID int64) ([]string, error) {
	query := `
		SELECT DISTINCT fs.source_url
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		ORDER BY fs.source_url`
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("query source urls for person %d: %w", personID, err)
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan source url: %w", err)
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
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

	// Try to find existing person first. Match accent- and case-insensitively
	// (via immutable_unaccent) so "éric zemmour" resolves to an existing
	// "eric zemmour" row instead of inserting a near-duplicate.
	var personID int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1)) LIMIT 1`,
		normalized,
	).Scan(&personID)
	if err == nil {
		return personID, nil
	}
	if err != pgx.ErrNoRows {
		return 0, fmt.Errorf("failed to query person: %w", err)
	}

	// Person is NEW — run it through the validator before inserting. A rejection
	// surfaces as *ErrEntityRejected so callers can soft-fail this fact and keep
	// going on the next one.
	if err := s.checkValidator(ctx, normalized, entityType); err != nil {
		return 0, err
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
		INSERT INTO facts (fact_text, fact_text_en, embedding_text, embedding, canonical_verb, lang)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6)
		RETURNING id
	`
	var factID int64
	err := s.pool.QueryRow(ctx, query,
		input.NormalizedFactText,
		input.NormalizedFactTextEn,
		input.EmbeddingText,
		pgvector.NewVector(input.Embedding),
		input.CanonicalVerb,
		input.Lang,
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

	// 2. Find existing persons. Match accent- and case-insensitively by folding
	//    BOTH sides in SQL (lower(immutable_unaccent(...))) so accent/case
	//    variants ("éric zemmour" vs "eric zemmour") — and unaccent-only folds
	//    Postgres applies but a Go fold does not (ß→ss, æ→ae, œ→oe, ł→l, …) —
	//    resolve to the same existing row. The lookup map is keyed by the
	//    normalized input name (as found in normalizedNamesList).
	existingPersons := make(map[string]int64) // Map normalized input name -> existing ID
	findStmt := `
		SELECT inp.n AS input_name, p.id
		FROM unnest($1::text[]) AS inp(n)
		JOIN persons p ON lower(immutable_unaccent(p.name)) = lower(immutable_unaccent(inp.n))`
	rows, err := tx.Query(ctx, findStmt, normalizedNamesList)
	if err != nil {
		return nil, fmt.Errorf("failed to query existing persons: %w", err)
	}
	defer rows.Close() // Ensure rows are closed even if scanning fails mid-way

	for rows.Next() {
		var id int64
		var inputName string
		if err := rows.Scan(&inputName, &id); err != nil {
			rows.Close() // Close immediately on error
			return nil, fmt.Errorf("failed to scan existing person row: %w", err)
		}
		// Keep the first match for a given input name (legacy dup rows that
		// share a folded key should be merged via `dm persons dedupe`).
		if _, seen := existingPersons[inputName]; !seen {
			existingPersons[inputName] = id
		}
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating existing person rows: %w", rows.Err())
	}
	rows.Close() // Explicit close after successful iteration

	// 3. Determine which persons need insertion. NEW candidates (not in existing
	//    map) go through the validator; rejected ones are logged and dropped so
	//    the rest of the batch keeps moving.
	namesToInsert := make([]string, 0)
	finalPersonIDsMap := make(map[string]int64) // Store all IDs (existing + new) by normalized name
	rejectedNames := make([]string, 0)
	for _, normalizedName := range normalizedNamesList {
		if id, exists := existingPersons[normalizedName]; exists {
			finalPersonIDsMap[normalizedName] = id
			continue
		}
		// New candidate — validate. Rejection drops it from this batch.
		if err := s.checkValidator(ctx, normalizedName, ""); err != nil {
			var rej *ErrEntityRejected
			if errors.As(err, &rej) {
				rejectedNames = append(rejectedNames, normalizedName)
				continue
			}
			// Non-rejection error (e.g. context cancelled) — bubble up; the batch
			// can't proceed safely if validation infrastructure itself is broken.
			return nil, fmt.Errorf("validator failed for %q: %w", normalizedName, err)
		}
		namesToInsert = append(namesToInsert, normalizedName)
	}

	// If every NEW candidate was rejected AND nothing pre-existed, surface a
	// single typed error so callers can decide whether this fact still has a
	// resolvable subject.
	if len(namesToInsert) == 0 && len(finalPersonIDsMap) == 0 && len(rejectedNames) > 0 {
		return nil, &ErrEntityRejected{
			Name:   rejectedNames[0],
			Reason: fmt.Sprintf("all %d candidate name(s) rejected by validator", len(rejectedNames)),
		}
	}

	// 4. Insert the new persons one row at a time with conflict-safe handling.
	//    These lists are small (a few involved-people names per fact). The
	//    `name` UNIQUE constraint is on the EXACT name, so ON CONFLICT (name)
	//    alone would NOT catch two new names in this same batch that differ only
	//    by accent ("eric" + "éric"). To prevent that, re-check by fold first —
	//    the lookup runs inside this tx, so it sees rows inserted earlier in this
	//    loop — and only insert when no fold match exists.
	for _, name := range namesToInsert {
		var id int64
		// Fold re-check: catches within-batch fold collisions (a row inserted
		// earlier in this loop) that the exact-name ON CONFLICT below would miss.
		err := tx.QueryRow(ctx,
			`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1)) LIMIT 1`,
			name,
		).Scan(&id)
		if err == nil {
			finalPersonIDsMap[name] = id
			continue
		}
		if err != pgx.ErrNoRows {
			return nil, fmt.Errorf("failed to re-check person %q by fold: %w", name, err)
		}

		err = tx.QueryRow(ctx,
			`INSERT INTO persons (name, entity_type) VALUES ($1, 'human')
			 ON CONFLICT (name) DO NOTHING
			 RETURNING id`,
			name,
		).Scan(&id)
		if err == pgx.ErrNoRows {
			// Exact-name race — resolve the existing id by fold.
			if err := tx.QueryRow(ctx,
				`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = lower(immutable_unaccent($1)) LIMIT 1`,
				name,
			).Scan(&id); err != nil {
				return nil, fmt.Errorf("failed to resolve conflicting person %q: %w", name, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("failed to insert new person %q: %w", name, err)
		} else {
			log.Debug().Int64("person_id", id).Str("name", name).Msg("Inserted new person")
		}
		finalPersonIDsMap[name] = id
	}

	// 6. Collect final list of IDs in the original order of unique normalized names.
	// Validator-rejected names are silently omitted (already logged in step 3 and
	// in checkValidator). The remaining missing-from-map case is a real bug.
	rejectedSet := make(map[string]struct{}, len(rejectedNames))
	for _, n := range rejectedNames {
		rejectedSet[n] = struct{}{}
	}
	personIDs = make([]int64, 0, len(normalizedNamesList))
	for _, normalizedName := range normalizedNamesList {
		if id, ok := finalPersonIDsMap[normalizedName]; ok {
			personIDs = append(personIDs, id)
			continue
		}
		if _, isRejected := rejectedSet[normalizedName]; isRejected {
			// Dropped on purpose — no id to return; caller sees a shorter slice.
			continue
		}
		log.Error().Str("normalized_name", normalizedName).Msg("Could not find final ID for person after insert/query steps")
		return nil, fmt.Errorf("internal error: could not find final ID for person '%s'", normalizedNameToOriginal[normalizedName])
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

	// Refresh materialized fact_count for each affected person
	for _, personID := range personIDs {
		if err := s.refreshPersonFactCount(ctx, tx, personID); err != nil {
			return err
		}
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

// queryPersonInfos runs a query selecting (id, name, entity_type) and scans the
// rows into PersonInfo. Backs the GetPersons* / list helpers below.
func (s *PostgresStorer) queryPersonInfos(ctx context.Context, query string, args ...any) ([]PersonInfo, error) {
	rows, err := s.pool.Query(ctx, query, args...)
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
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}
	return persons, nil
}

// GetAllPersons retrieves all persons with their details (id, name, entity_type).
func (s *PostgresStorer) GetAllPersons(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, `SELECT id, name, COALESCE(entity_type, 'person') as entity_type FROM persons ORDER BY name`)
}

// GetPersonsWithMultipleStances returns only the persons who have at least 2
// stance documents. Used by the daily merge sweep to avoid the cost of
// inspecting the thousands of single-stance / no-stance persons that can't
// possibly produce a merge candidate.
func (s *PostgresStorer) GetPersonsWithMultipleStances(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN (
			SELECT person_id FROM stance_documents
			GROUP BY person_id HAVING COUNT(*) >= 2
		) s ON s.person_id = p.id
		ORDER BY p.name`)
}

// GetPersonsWithFacts returns only persons that have at least one fact attached
// via fact_mentions. Used by the stance rebuild/backfill/assign-stances paths to
// skip the long tail of zero-fact persons (~2800/4000 in prod as of 2026-05),
// which would otherwise eat per-person init time (DB connection, validator
// state, logging) for no useful work — backfillStancesForPerson short-circuits
// inside the loop when facts is empty, but the wrapper still pays the cost.
// Filtering at SQL level removes that overhead.
func (s *PostgresStorer) GetPersonsWithFacts(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		WHERE EXISTS (
			SELECT 1 FROM fact_mentions fm WHERE fm.person_id = p.id
		)
		ORDER BY p.name`)
}

// AddPerson inserts a new person into the persons table if they don't already exist.
// It uses the normalized version of the name. New candidates (those not already
// in the table) are run through the EntityValidator first; rejected candidates
// surface as *ErrEntityRejected so callers can decide whether to skip them.
func (s *PostgresStorer) AddPerson(ctx context.Context, name string) (id int64, inserted bool, err error) {
	normalizedName := util.NormalizeString(name)
	if normalizedName == "" {
		return 0, false, fmt.Errorf("cannot add empty person name")
	}

	// Fast path: if the person already exists, skip validation (they were validated
	// when first inserted OR pre-date validation). Validate only NEW candidates.
	if existing, lookupErr := s.findPersonIDByName(ctx, normalizedName); lookupErr == nil && existing > 0 {
		return existing, false, nil
	}
	if err := s.checkValidator(ctx, normalizedName, ""); err != nil {
		return 0, false, err
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

	// Fast path: existing persons skip validation; only NEW candidates get checked.
	if existing, lookupErr := s.findPersonIDByName(ctx, normalizedName); lookupErr == nil && existing > 0 {
		return existing, false, nil
	}
	if err := s.checkValidator(ctx, normalizedName, entityType); err != nil {
		return 0, false, err
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

	// Query returns active (non-duplicate) facts within the similarity threshold (LIMIT 30)
	// This caps KNN results to prevent unbounded cost growth as facts accumulate
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
            f.embedding <-> $2 <= $4 -- Similarity threshold
        AND NOT EXISTS (
            SELECT 1 FROM fact_relations fr
            WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
        )
        ORDER BY
            distance
        LIMIT 30`

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

// loadPairSet returns a set of normalized "factAID-factBID" keys (smaller ID
// first, as stored) from a table holding (fact_a_id, fact_b_id) columns for a
// person. Backs GetExistingContradictionPairs and GetCheckedPairs.
func (s *PostgresStorer) loadPairSet(ctx context.Context, table string, personID int64) (map[string]bool, error) {
	query := fmt.Sprintf(`SELECT fact_a_id, fact_b_id FROM %s WHERE person_id = $1`, table)
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s for person %d: %w", table, personID, err)
	}
	defer rows.Close()

	pairs := make(map[string]bool)
	for rows.Next() {
		var factAID, factBID int64
		if err := rows.Scan(&factAID, &factBID); err != nil {
			return nil, fmt.Errorf("failed to scan pair from %s: %w", table, err)
		}
		pairs[fmt.Sprintf("%d-%d", factAID, factBID)] = true
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating pairs from %s: %w", table, rows.Err())
	}
	return pairs, nil
}

// GetExistingContradictionPairs returns a map of existing contradiction pairs for a person.
// The map keys are "factAID-factBID" strings (with smaller ID first) for quick lookup.
func (s *PostgresStorer) GetExistingContradictionPairs(ctx context.Context, personID int64) (map[string]bool, error) {
	return s.loadPairSet(ctx, "contradictions", personID)
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
	return s.loadPairSet(ctx, "fact_pair_checks", personID)
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
//
// Returns:
//   - contradictionID: the row id (whether newly inserted or pre-existing)
//   - needsBundler: true iff this insert produced a NULL bundle_stance_id (cross-stance
//     or unstanced facts) — callers should pass this row to the contradiction Bundler
//     for LLM-driven stance assignment. False for same-stance fast-path bundles and for
//     pre-existing rows (which already had a chance to be bundled).
func (s *PostgresStorer) RecordContradictionImmediate(ctx context.Context, personID int64, factAID int64, factBID int64, reason string, reasonEn string) (contradictionID int64, needsBundler bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	id, wasInserted, bundleStanceID, err := s.RecordContradiction(ctx, tx, personID, factAID, factBID, reason, reasonEn)
	if err != nil {
		return 0, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("failed to commit contradiction: %w", err)
	}

	return id, wasInserted && bundleStanceID == nil, nil
}

// RecordContradiction inserts a record indicating that two facts for a person are contradictory.
// Ensures that factAID < factBID to avoid duplicate pairs in different orders.
// Now includes a reason for the contradiction.
// On new contradiction insertion, recomputes the person's honesty score and emits events.
// Returns the contradiction id, whether it was newly inserted, and the bundle_stance_id
// (nil = needs bundler resolution).
func (s *PostgresStorer) RecordContradiction(ctx context.Context, tx pgx.Tx, personID int64, factAID int64, factBID int64, reason string, reasonEn string) (int64, bool, *int64, error) {
	timer := metrics.NewQueryTimer("record_contradiction")

	// Ensure consistent ordering of fact IDs to prevent duplicates like (1,2) and (2,1)
	if factAID > factBID {
		factAID, factBID = factBID, factAID
	}

	// Fast-path bundle resolution: bundle_stance_id is set to the stance shared by both
	// facts (if any). Cross-stance and unstanced pairs get NULL and behave as singleton
	// solo bundles in the score until an LLM-driven Bundler pass upgrades them.
	query := `
        WITH shared AS (
            SELECT sa.stance_document_id
            FROM stance_document_facts sa
            JOIN stance_document_facts sb ON sb.fact_id = $3
                                          AND sb.stance_document_id = sa.stance_document_id
            WHERE sa.fact_id = $2
            LIMIT 1
        )
        INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason, reason_en, bundle_stance_id)
        VALUES ($1, $2, $3, $4, NULLIF($5,''), (SELECT stance_document_id FROM shared))
        ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
        SET reason = EXCLUDED.reason,
            reason_en = EXCLUDED.reason_en,
            detected_at = CURRENT_TIMESTAMP,
            -- Only fill in bundle_stance_id if it's still NULL; never clobber an
            -- LLM-resolved override on re-detection.
            bundle_stance_id = COALESCE(contradictions.bundle_stance_id, EXCLUDED.bundle_stance_id)
        RETURNING id, (xmax = 0) AS was_inserted, bundle_stance_id`

	var contradictionID int64
	var wasInserted bool
	var bundleStanceID *int64
	err := tx.QueryRow(ctx, query, personID, factAID, factBID, reason, reasonEn).Scan(&contradictionID, &wasInserted, &bundleStanceID)
	timer.ObserveDuration(err)

	if err != nil {
		return 0, false, nil, fmt.Errorf("failed to insert/update contradiction for person %d between facts %d and %d: %w", personID, factAID, factBID, err)
	}

	if wasInserted && bundleStanceID == nil {
		log.Warn().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).
			Msg("Contradiction recorded with no bundle_stance_id (facts cross-stance or unstanced) — run dm stances rebuild and the Bundler to group it")
	}

	log.Info().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).Bool("was_new_contradiction", wasInserted).Interface("bundle_stance_id", bundleStanceID).Msg("Recorded/updated contradiction")

	// If this was a new contradiction, recompute honesty score and emit events
	if wasInserted {
		metrics.IncrementContradictionsDetected()
		if err := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, personID, contradictionID); err != nil {
			return 0, false, nil, fmt.Errorf("failed to recompute honesty score and emit events: %w", err)
		}
	}

	return contradictionID, wasInserted, bundleStanceID, nil
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

// GetFactTextsByIDs returns fact_text for each id, keyed by id. Missing facts are absent
// from the map. Used by the batch contradiction poller to feed the Bundler with claim text
// when only ids are available from the parsed custom-id payload.
func (s *PostgresStorer) GetFactTextsByIDs(ctx context.Context, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, fact_text FROM facts WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying fact texts: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, fmt.Errorf("scanning fact text row: %w", err)
		}
		out[id] = text
	}
	return out, rows.Err()
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

// HonestyScoreRatioDecay controls the exponential penalty based on the bundle/facts ratio.
// Kept moderate (was 4) so a single contradiction in a tiny fact base does not crush the score.
const HonestyScoreRatioDecay = 1.5

// HonestyScoreAbsoluteDecay controls the exponential penalty per distinct contradiction bundle.
// Each bundle contributes one full penalty regardless of how many raw pairs it contains.
// Sized so that one contradiction yields ~95% honesty and the score decays exponentially
// from there as more contradictions are added.
const HonestyScoreAbsoluteDecay = 0.03

// HonestyScoreBundleExtraDecay controls the (logarithmic) penalty for repeated contradictions
// inside the same bundle. extras = total raw pairs - distinct bundles. The constant 0.05 is
// chosen so that any bundle of size N >= 2 strictly reduces the penalty vs. counting each pair
// independently.
const HonestyScoreBundleExtraDecay = 0.05

// HonestyScoreMinDenominator is the Bayesian-style smoothing floor for the ratio term:
// ratio = bundles / max(facts, HonestyScoreMinDenominator). Without it, a person with
// 1 contradiction out of 2 facts gets crushed even though the sample size is too small
// to justify that confidence. The floor of 75 keeps a single contradiction near ~95%
// honesty until enough facts accumulate to confirm the ratio is real.
const HonestyScoreMinDenominator = 75

// ContradictionStats summarises a person's contradictions after stance bundling.
// Bundles is the number of distinct stance-grouped contradictions; Total is the raw pair count.
// Total >= Bundles always; extras := Total - Bundles is the repetition penalty input.
type ContradictionStats struct {
	Bundles int
	Total   int
}

// ComputeHonestyScore returns exp(-k * bundles/denom - j * bundles - alpha * ln(1 + extras)),
// where denom = max(facts, HonestyScoreMinDenominator). Bundling collapses raw pairs that
// share an underlying stance (topic) into one underlying inconsistency, so 10 paraphrase
// pairs about the same broken promise count as one bundle plus a small repetition penalty
// rather than 10 independent contradictions. The smoothing floor prevents one contradiction
// at very low fact counts from collapsing the score.
// If factsCount is 0, returns 1.0 (benefit of the doubt).
func ComputeHonestyScore(factsCount int, stats ContradictionStats) float64 {
	if factsCount == 0 {
		return 1.0
	}
	extras := stats.Total - stats.Bundles
	if extras < 0 {
		extras = 0
	}
	denom := factsCount
	if denom < HonestyScoreMinDenominator {
		denom = HonestyScoreMinDenominator
	}
	ratio := float64(stats.Bundles) / float64(denom)
	return math.Exp(
		-HonestyScoreRatioDecay*ratio -
			HonestyScoreAbsoluteDecay*float64(stats.Bundles) -
			HonestyScoreBundleExtraDecay*math.Log1p(float64(extras)))
}

// ComputeAggregateScore returns the aggregate honesty score for an entity.
// If membersScore is nil (no members), returns ownScore directly.
// Otherwise returns 0.5*ownScore + 0.5*membersScore.
func ComputeAggregateScore(ownScore float64, membersScore *float64) float64 {
	if membersScore == nil {
		return ownScore
	}
	return 0.5*ownScore + 0.5*(*membersScore)
}

// recomputeHonestyScoreAndEmitEvents recomputes the honesty score for a person and emits relevant events
func (s *PostgresStorer) recomputeHonestyScoreAndEmitEvents(ctx context.Context, tx pgx.Tx, personID int64, contradictionID int64) error {
	factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get facts count for person %d: %w", personID, err)
	}

	stats, err := s.getPersonContradictionStats(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get contradiction stats for person %d: %w", personID, err)
	}

	ownScore := ComputeHonestyScore(factsCount, stats)

	// Check entity type to determine if we need members_score
	var entityType *string
	err = tx.QueryRow(ctx, `SELECT entity_type FROM persons WHERE id = $1`, personID).Scan(&entityType)
	if err != nil {
		return fmt.Errorf("failed to get entity_type for person %d: %w", personID, err)
	}

	var membersScore *float64
	if entityType != nil && *entityType != "human" {
		membersScore, err = s.computeMembersScore(ctx, tx, personID)
		if err != nil {
			return fmt.Errorf("failed to compute members score for person %d: %w", personID, err)
		}
	}

	aggregate := ComputeAggregateScore(ownScore, membersScore)

	if err := s.updatePersonHonestyScore(ctx, tx, personID, ownScore, membersScore, aggregate); err != nil {
		return fmt.Errorf("failed to update honesty score for person %d: %w", personID, err)
	}

	if err := s.refreshPersonFactCount(ctx, tx, personID); err != nil {
		return fmt.Errorf("failed to refresh fact_count for person %d: %w", personID, err)
	}

	metrics.ObserveHonestyScore(aggregate)

	bundleID, bundleSize, err := s.getContradictionBundleInfo(ctx, tx, personID, contradictionID)
	if err != nil {
		return fmt.Errorf("failed to read bundle info for contradiction %d: %w", contradictionID, err)
	}

	payload := map[string]interface{}{
		"contradiction_id": contradictionID,
		"honesty_score":    aggregate,
		"bundle_id":        bundleID,
		"bundle_size":      bundleSize,
	}

	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := s.insertEvent(ctx, tx, "contradiction_added", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit contradiction_added event: %w", err)
	}

	log.Info().Int64("person_id", personID).Float64("own_score", ownScore).Interface("members_score", membersScore).Float64("aggregate", aggregate).Int("facts_count", factsCount).Int("bundles", stats.Bundles).Int("contradictions_total", stats.Total).Msg("Recomputed honesty score and emitted events")

	// Cascade: recompute parent entity scores
	if err := s.recomputeParentEntityScores(ctx, tx, personID); err != nil {
		return fmt.Errorf("failed to recompute parent entity scores: %w", err)
	}

	return nil
}

// computeMembersScore calculates the average honesty_score of active members for an entity.
// Returns nil if no active members exist.
func (s *PostgresStorer) computeMembersScore(ctx context.Context, tx pgx.Tx, entityID int64) (*float64, error) {
	query := `
		SELECT AVG(p.honesty_score) FROM persons p
		JOIN person_links pl ON p.id = pl.src_person_id
		WHERE pl.dst_person_id = $1
		  AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)`

	var avg *float64
	err := tx.QueryRow(ctx, query, entityID).Scan(&avg)
	if err != nil {
		return nil, fmt.Errorf("failed to compute members score for entity %d: %w", entityID, err)
	}
	return avg, nil
}

// recomputeParentEntityScores recomputes members_score and honesty_score for all parent entities
// of the given person (via person_links where src_person_id = personID).
func (s *PostgresStorer) recomputeParentEntityScores(ctx context.Context, tx pgx.Tx, personID int64) error {
	query := `SELECT DISTINCT dst_person_id FROM person_links WHERE src_person_id = $1 AND (valid_to IS NULL OR valid_to >= CURRENT_DATE)`
	rows, err := tx.Query(ctx, query, personID)
	if err != nil {
		return fmt.Errorf("failed to find parent entities for person %d: %w", personID, err)
	}
	defer rows.Close()

	var parentIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan parent entity ID: %w", err)
		}
		parentIDs = append(parentIDs, id)
	}
	if rows.Err() != nil {
		return fmt.Errorf("error iterating parent entities: %w", rows.Err())
	}

	for _, parentID := range parentIDs {
		membersScore, err := s.computeMembersScore(ctx, tx, parentID)
		if err != nil {
			return err
		}

		// Get the entity's own_score
		var ownScore float64
		err = tx.QueryRow(ctx, `SELECT own_score FROM persons WHERE id = $1`, parentID).Scan(&ownScore)
		if err != nil {
			return fmt.Errorf("failed to get own_score for entity %d: %w", parentID, err)
		}

		aggregate := ComputeAggregateScore(ownScore, membersScore)

		if err := s.updatePersonHonestyScore(ctx, tx, parentID, ownScore, membersScore, aggregate); err != nil {
			return err
		}

		if err := s.insertEvent(ctx, tx, "person_updated", "person", parentID, map[string]interface{}{
			"honesty_score": aggregate,
			"own_score":     ownScore,
			"members_score": membersScore,
		}); err != nil {
			return fmt.Errorf("failed to emit person_updated event for entity %d: %w", parentID, err)
		}

		log.Info().Int64("entity_id", parentID).Float64("own_score", ownScore).Interface("members_score", membersScore).Float64("aggregate", aggregate).Msg("Recomputed parent entity scores")
	}

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

// getContradictionBundleInfo returns (bundleID, bundleSize) for a contradiction id.
// bundleID is *int64 — nil for solo (no bundle_stance_id). bundleSize counts pairs in the
// bundle (1 for solo). Used to enrich SSE payloads with bundle context.
func (s *PostgresStorer) getContradictionBundleInfo(ctx context.Context, tx pgx.Tx, personID int64, contradictionID int64) (*int64, int, error) {
	query := `
		WITH target AS (
			SELECT bundle_stance_id FROM contradictions WHERE id = $1
		)
		SELECT
			(SELECT bundle_stance_id FROM target),
			CASE WHEN (SELECT bundle_stance_id FROM target) IS NULL THEN 1
				 ELSE (SELECT COUNT(*) FROM contradictions
					   WHERE person_id = $2
						 AND bundle_stance_id = (SELECT bundle_stance_id FROM target))
			END AS bundle_size`
	var bundleID *int64
	var bundleSize int
	if err := tx.QueryRow(ctx, query, contradictionID, personID).Scan(&bundleID, &bundleSize); err != nil {
		return nil, 0, fmt.Errorf("failed to query bundle info for contradiction %d: %w", contradictionID, err)
	}
	return bundleID, bundleSize, nil
}

// getPersonContradictionStats returns (bundles, total) for a person, where bundles is the
// number of distinct stance-grouped clusters of contradictions and total is the raw pair count.
// Rows whose bundle_stance_id is NULL (cross-stance, or facts predating stance backfill)
// are each their own singleton bundle. See ComputeHonestyScore for how these feed the score.
func (s *PostgresStorer) getPersonContradictionStats(ctx context.Context, tx pgx.Tx, personID int64) (ContradictionStats, error) {
	query := `
		SELECT
			COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:'||id::text)) AS bundles,
			COUNT(*) AS total
		FROM contradictions
		WHERE person_id = $1`

	var stats ContradictionStats
	err := tx.QueryRow(ctx, query, personID).Scan(&stats.Bundles, &stats.Total)
	if err != nil {
		return ContradictionStats{}, fmt.Errorf("failed to compute contradiction stats for person %d: %w", personID, err)
	}
	return stats, nil
}

// GetStanceContradictionCounts returns per-stance contradiction totals for a person,
// keyed by stance_documents.id. Stances with zero contradictions are omitted (callers
// should treat missing keys as zero). Used by the stance-first profile UI to compute
// per-stance honesty scores that match the global formula.
func (s *PostgresStorer) GetStanceContradictionCounts(ctx context.Context, personID int64) (map[int64]int, error) {
	query := `
		SELECT bundle_stance_id, COUNT(*)
		FROM contradictions
		WHERE person_id = $1 AND bundle_stance_id IS NOT NULL
		GROUP BY bundle_stance_id`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query stance contradiction counts for person %d: %w", personID, err)
	}
	defer rows.Close()

	counts := make(map[int64]int)
	for rows.Next() {
		var stanceID int64
		var n int
		if err := rows.Scan(&stanceID, &n); err != nil {
			return nil, fmt.Errorf("failed to scan stance contradiction count: %w", err)
		}
		counts[stanceID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating stance contradiction counts: %w", err)
	}
	return counts, nil
}

// refreshPersonFactCount recomputes and stores the deduped fact count for a person.
// Must be called within a transaction. This keeps persons.fact_count in sync after
// fact insertions, deletions, or dedup relation changes.
func (s *PostgresStorer) refreshPersonFactCount(ctx context.Context, tx pgx.Tx, personID int64) error {
	query := `
		UPDATE persons SET fact_count = (
			SELECT COUNT(DISTINCT fs.fact_id) FILTER (
				WHERE NOT EXISTS (
					SELECT 1 FROM fact_relations fr
					WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate'
				)
			)
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
		)
		WHERE id = $1`
	_, err := tx.Exec(ctx, query, personID)
	if err != nil {
		return fmt.Errorf("failed to refresh fact_count for person %d: %w", personID, err)
	}
	return nil
}

// updatePersonHonestyScore updates own_score, members_score, and aggregate honesty_score for a person
func (s *PostgresStorer) updatePersonHonestyScore(ctx context.Context, tx pgx.Tx, personID int64, ownScore float64, membersScore *float64, aggregate float64) error {
	query := `UPDATE persons SET own_score = $1, members_score = $2, honesty_score = $3 WHERE id = $4`

	_, err := tx.Exec(ctx, query, ownScore, membersScore, aggregate, personID)
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
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	EntityType         *string  `json:"entity_type,omitempty"`
	HonestyScore       float64  `json:"honesty_score"`
	OwnScore           *float64 `json:"own_score,omitempty"`
	MembersScore       *float64 `json:"members_score,omitempty"`
	ImageURL           *string  `json:"image_url,omitempty"`
	FactCount          int      `json:"fact_count"`
	StanceCount        int      `json:"stance_count"`
	ContradictionCount int      `json:"contradiction_count"`
}

// GetPersonsRankedByScore returns persons ranked by the chosen sort order.
// entityType can be empty (all), "party", or "human" to filter results.
// search performs case-insensitive partial match on name.
// sort can be "honesty_desc" (highest score first), "honesty_asc" (lowest first),
// or empty for the default (fact count desc, then honesty desc).
// minFacts filters out persons with fewer than minFacts (deduped) facts.
func (s *PostgresStorer) GetPersonsRankedByScore(ctx context.Context, limit int, offset int, entityType string, search string, sort string, minFacts int) ([]PersonSummary, error) {
	var orderBy string
	switch sort {
	case "honesty_desc":
		orderBy = "p.honesty_score DESC, fact_count DESC, p.name ASC"
	case "honesty_asc":
		orderBy = "p.honesty_score ASC, fact_count DESC, p.name ASC"
	default:
		orderBy = "fact_count DESC, p.honesty_score DESC, p.name ASC"
	}

	query := fmt.Sprintf(`
		SELECT
			p.id,
			p.name,
			p.entity_type,
			p.honesty_score,
			p.own_score,
			p.members_score,
			p.image_url,
			p.fact_count
		FROM persons p
		WHERE ($3 = '' OR p.entity_type = $3)
		  AND ($4 = '' OR p.name ILIKE '%%' || $4 || '%%')
		  AND p.fact_count >= $5
		ORDER BY %s
		LIMIT $1 OFFSET $2`, orderBy)

	rows, err := s.pool.Query(ctx, query, limit, offset, entityType, search, minFacts)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []PersonSummary
	for rows.Next() {
		var person PersonSummary
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.OwnScore, &person.MembersScore, &person.ImageURL, &person.FactCount)
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

// GetPersonLang returns the person's BCP-47 source language ("fr", "en", "de", ...)
// from persons.lang. Used by the news plugin to localise outbound search queries
// — French politicians get fr-language searches, German get de-language, etc.,
// with an en pass added in addition. Returns "fr" as a safe fallback if the
// person doesn't exist or has no lang set (matches the column default).
func (s *PostgresStorer) GetPersonLang(ctx context.Context, personID int64) (string, error) {
	var lang string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(lang, 'fr') FROM persons WHERE id = $1`, personID).Scan(&lang)
	if err == pgx.ErrNoRows {
		return "fr", nil
	}
	if err != nil {
		return "fr", fmt.Errorf("get person lang: %w", err)
	}
	if lang == "" {
		return "fr", nil
	}
	return lang, nil
}

// GetPersonByID returns a single person by ID, including stance and contradiction
// counts used for the profile-page summary line.
func (s *PostgresStorer) GetPersonByID(ctx context.Context, personID int64) (*PersonSummary, error) {
	query := `
		SELECT
			p.id,
			p.name,
			p.entity_type,
			p.honesty_score,
			p.own_score,
			p.members_score,
			p.image_url,
			COUNT(DISTINCT fs.fact_id) FILTER (
				WHERE NOT EXISTS (SELECT 1 FROM fact_relations fr WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate')
			) AS fact_count,
			COALESCE((SELECT COUNT(*) FROM stance_documents WHERE person_id = p.id), 0) AS stance_count,
			COALESCE((SELECT COUNT(*) FROM contradictions  WHERE person_id = p.id), 0) AS contradiction_count
		FROM persons p
		LEFT JOIN fact_mentions fm ON p.id = fm.person_id
		LEFT JOIN fact_sources fs ON fm.fact_source_id = fs.id
		WHERE p.id = $1
		GROUP BY p.id, p.name, p.entity_type, p.honesty_score, p.own_score, p.members_score, p.image_url`

	var person PersonSummary
	err := s.pool.QueryRow(ctx, query, personID).Scan(
		&person.ID, &person.Name, &person.EntityType, &person.HonestyScore,
		&person.OwnScore, &person.MembersScore, &person.ImageURL,
		&person.FactCount, &person.StanceCount, &person.ContradictionCount)
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
	FactID        int64   `json:"fact_id"`
	FactText      string  `json:"fact_text"`
	FactTextEn    string  `json:"fact_text_en,omitempty"` // English translation; empty if source is English
	Lang          string  `json:"lang,omitempty"`         // BCP-47 source language
	SourceURL     string  `json:"source_url"`
	OriginalQuote string  `json:"original_quote"`
	ArchiveURL    *string `json:"archive_url,omitempty"` // Wayback snapshot URL; nil until archived
}

// SourceToArchive is a fact source row that still needs a Wayback snapshot.
type SourceToArchive struct {
	ID        int64
	SourceURL string
}

// GetSourcesWithoutArchiveURL returns up to limit fact_sources that have never been
// archived (archive_url IS NULL) and whose last attempt is older than the cooldown,
// so the background worker neither skips fresh sources nor retries failures too soon.
func (s *PostgresStorer) GetSourcesWithoutArchiveURL(ctx context.Context, limit int) ([]SourceToArchive, error) {
	query := `
		SELECT id, source_url
		FROM fact_sources
		WHERE source_url IS NOT NULL AND source_url <> ''
		  AND archive_url IS NULL
		  AND (archive_attempted_at IS NULL OR archive_attempted_at < NOW() - INTERVAL '7 days')
		ORDER BY id
		LIMIT $1`

	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query sources without archive URL: %w", err)
	}
	defer rows.Close()

	var out []SourceToArchive
	for rows.Next() {
		var r SourceToArchive
		if err := rows.Scan(&r.ID, &r.SourceURL); err != nil {
			return nil, fmt.Errorf("scan source row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateSourceArchiveURL records the result of an archiving attempt. archiveURL is nil
// when the save request was submitted but the snapshot is not yet available; the
// archive_attempted_at timestamp is always updated so we do not retry within the cooldown.
func (s *PostgresStorer) UpdateSourceArchiveURL(ctx context.Context, sourceID int64, archiveURL *string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fact_sources
		SET archive_url = $2, archive_attempted_at = NOW()
		WHERE id = $1`,
		sourceID, archiveURL)
	if err != nil {
		return fmt.Errorf("update archive url for source %d: %w", sourceID, err)
	}
	return nil
}

// FactInStance is the projection returned by /api/stances/:id/facts. Carries
// both the native + English versions so the frontend language toggle can swap
// per-fact without a second round trip.
type FactInStance struct {
	FactID        int64   `json:"fact_id"`
	FactText      string  `json:"fact_text"`
	FactTextEn    string  `json:"fact_text_en,omitempty"`
	Lang          string  `json:"lang,omitempty"`
	CanonicalVerb string  `json:"canonical_verb,omitempty"`
	SourceURL     string  `json:"source_url"`
	OriginalQuote string  `json:"original_quote,omitempty"`
	ArchiveURL    *string `json:"archive_url,omitempty"` // Wayback snapshot URL; nil until archived
}

// GetFactsInStance returns the facts attached to a stance with both native and
// English text + source info, for the per-stance Facts tab on the person page.
func (s *PostgresStorer) GetFactsInStance(ctx context.Context, stanceID int64) ([]FactInStance, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.fact_text_en,'') AS fact_text_en,
		       COALESCE(f.lang,'') AS lang, COALESCE(f.canonical_verb,'') AS canonical_verb,
		       src.source_url, COALESCE(src.original_quote,'') AS original_quote, src.archive_url
		FROM facts f
		LEFT JOIN LATERAL (
		    SELECT fs.source_url, fs.original_quote, fs.archive_url
		    FROM fact_sources fs WHERE fs.fact_id = f.id ORDER BY fs.id LIMIT 1
		) src ON true
		JOIN stance_document_facts sdf ON sdf.fact_id = f.id
		WHERE sdf.stance_document_id = $1
		ORDER BY f.id`
	rows, err := s.pool.Query(ctx, query, stanceID)
	if err != nil {
		return nil, fmt.Errorf("GetFactsInStance(%d): %w", stanceID, err)
	}
	defer rows.Close()
	out := make([]FactInStance, 0)
	for rows.Next() {
		var f FactInStance
		var src *string
		if err := rows.Scan(&f.FactID, &f.FactText, &f.FactTextEn, &f.Lang, &f.CanonicalVerb, &src, &f.OriginalQuote, &f.ArchiveURL); err != nil {
			return nil, err
		}
		if src != nil {
			f.SourceURL = *src
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PaginatedFacts wraps a page of facts with the total count.
type PaginatedFacts struct {
	Items []FactWithSource `json:"items"`
	Total int              `json:"total"`
}

// PaginatedContradictions wraps a page of contradictions with the total count.
type PaginatedContradictions struct {
	Items []ContradictionWithFacts `json:"items"`
	Total int                      `json:"total"`
}

// GetPersonFacts returns a page of facts for a person together with the total count.
func (s *PostgresStorer) GetPersonFacts(ctx context.Context, personID int64, limit int, offset int) (*PaginatedFacts, error) {
	query := `
		WITH fact_rows AS (
			SELECT DISTINCT f.id, f.fact_text, COALESCE(f.fact_text_en, '') AS fact_text_en, COALESCE(f.lang, '') AS lang, fs.source_url, fs.original_quote, fs.archive_url
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
			  AND NOT EXISTS (SELECT 1 FROM fact_relations fr WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')
		)
		SELECT id, fact_text, fact_text_en, lang, source_url, original_quote, archive_url,
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
		err := rows.Scan(&fact.FactID, &fact.FactText, &fact.FactTextEn, &fact.Lang, &fact.SourceURL, &fact.OriginalQuote, &fact.ArchiveURL, &result.Total)
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
			WHERE fm.person_id = $1
			  AND NOT EXISTS (SELECT 1 FROM fact_relations fr WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')`
		if err := s.pool.QueryRow(ctx, countQuery, personID).Scan(&result.Total); err != nil {
			return nil, fmt.Errorf("failed to count facts for person %d: %w", personID, err)
		}
	}

	return result, nil
}

// ContradictionWithFacts represents a contradiction with both fact texts, sources, and dates.
// Bundle fields (BundleID/BundleLabel/BundleSummary/BundleSize) describe the stance bundle
// this contradiction belongs to, used by the frontend to group paraphrase pairs under one card.
// BundleID is nil for "solo" rows (cross-stance or unstanced).
// FactAStanceLabel/FactBStanceLabel surface each fact's own stance label so the frontend can
// render a "topic A ↔ topic B" header on cross-stance solos and a data-quality badge if either
// is nil (which means a fact is missing its stance row — should never happen post-migration).
type ContradictionWithFacts struct {
	ID                 int64      `json:"id"`
	PersonID           int64      `json:"person_id"`
	FactAID            int64      `json:"fact_a_id"`
	FactBID            int64      `json:"fact_b_id"`
	FactAText          string     `json:"fact_a_text"`
	FactAtextEn        string     `json:"fact_a_text_en,omitempty"`
	FactBText          string     `json:"fact_b_text"`
	FactBtextEn        string     `json:"fact_b_text_en,omitempty"`
	FactASourceURLs    []string   `json:"fact_a_source_urls"`
	FactBSourceURLs    []string   `json:"fact_b_source_urls"`
	FactADate          *time.Time `json:"fact_a_date"`
	FactBDate          *time.Time `json:"fact_b_date"`
	Reason             string     `json:"reason"`
	ReasonEn           string     `json:"reason_en,omitempty"`
	DetectedAt         time.Time  `json:"detected_at"`
	BundleID           *int64     `json:"bundle_id"`
	BundleLabel        *string    `json:"bundle_label"`
	BundleLabelEn      *string    `json:"bundle_label_en,omitempty"`
	BundleSummary      *string    `json:"bundle_summary"`
	BundleSummaryEn    *string    `json:"bundle_summary_en,omitempty"`
	BundleSize         int        `json:"bundle_size"`
	FactAStanceID      *int64     `json:"fact_a_stance_id"`
	FactBStanceID      *int64     `json:"fact_b_stance_id"`
	FactAStanceLabel   *string    `json:"fact_a_stance_label"`
	FactAStanceLabelEn *string    `json:"fact_a_stance_label_en,omitempty"`
	FactBStanceLabel   *string    `json:"fact_b_stance_label"`
	FactBStanceLabelEn *string    `json:"fact_b_stance_label_en,omitempty"`
}

// ContradictionAuditRow is a contradiction enriched with the person name,
// used by the audit tool which may span all persons in the DB.
type ContradictionAuditRow struct {
	ContradictionWithFacts
	PersonName string `json:"person_name"`
}

// ListContradictions returns recorded contradictions, optionally filtered to a
// single person, optionally ordered randomly (for sampled audits), optionally
// capped at `limit` rows (limit <= 0 means no cap). It joins facts +
// fact_sources + persons in a single query to avoid N round-trips.
func (s *PostgresStorer) ListContradictions(ctx context.Context, personID *int64, random bool, limit int) ([]ContradictionAuditRow, error) {
	var (
		whereClause string
		orderClause string
		args        []any
	)
	if personID != nil {
		whereClause = "WHERE c.person_id = $1"
		args = append(args, *personID)
	}
	if random {
		orderClause = "ORDER BY RANDOM()"
	} else {
		orderClause = "ORDER BY c.detected_at DESC"
	}
	if limit > 0 {
		args = append(args, limit)
		orderClause += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	query := fmt.Sprintf(`
		SELECT c.id, c.person_id, p.name AS person_name,
		       c.fact_a_id, c.fact_b_id,
		       fa.fact_text AS fact_a_text, fb.fact_text AS fact_b_text,
		       COALESCE(fsa.urls, '{}') AS fact_a_urls, COALESCE(fsb.urls, '{}') AS fact_b_urls,
		       COALESCE(fsa.earliest, fa.created_at) AS fact_a_date,
		       COALESCE(fsb.earliest, fb.created_at) AS fact_b_date,
		       c.reason, c.detected_at
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
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
		%s
		%s`, whereClause, orderClause)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query contradictions: %w", err)
	}
	defer rows.Close()

	out := make([]ContradictionAuditRow, 0)
	for rows.Next() {
		var r ContradictionAuditRow
		if err := rows.Scan(
			&r.ID, &r.PersonID, &r.PersonName,
			&r.FactAID, &r.FactBID,
			&r.FactAText, &r.FactBText,
			&r.FactASourceURLs, &r.FactBSourceURLs,
			&r.FactADate, &r.FactBDate,
			&r.Reason, &r.DetectedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan contradiction row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating contradictions: %w", err)
	}
	return out, nil
}

// GetPersonContradictions returns a page of contradictions for a person together with the total count.
func (s *PostgresStorer) GetPersonContradictions(ctx context.Context, personID int64, limit int, offset int) (*PaginatedContradictions, error) {
	// Bundle key is the contradiction's bundle_stance_id (set at insert by the fast path
	// or upgraded later by the Bundler). Solo rows (NULL) become singleton bundles via the
	// 'solo:'||id key. bundle_size counts pairs sharing the same bundle key for this person,
	// so the frontend can render one card per bundle with a "N contradictions" badge.
	// Per-fact stance is pulled via stance_document_facts → stance_documents so the frontend
	// can render "topic A ↔ topic B" headers on cross-stance solos (bundle_id NULL but both
	// fact stance ids non-null) and a data-quality badge when either is NULL (post-migration
	// invariant violation: a fact landed without a stance).
	query := `
		WITH contradiction_rows AS (
			SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
				   fa.fact_text AS fact_a_text, COALESCE(fa.fact_text_en,'') AS fact_a_text_en,
				   fb.fact_text AS fact_b_text, COALESCE(fb.fact_text_en,'') AS fact_b_text_en,
				   COALESCE(fsa.urls, '{}') AS fact_a_urls, COALESCE(fsb.urls, '{}') AS fact_b_urls,
				   COALESCE(fsa.earliest, fa.created_at) AS fact_a_date,
				   COALESCE(fsb.earliest, fb.created_at) AS fact_b_date,
				   c.reason, COALESCE(c.reason_en,'') AS reason_en, c.detected_at,
				   c.bundle_stance_id,
				   sd.cluster_label AS bundle_label,
				   sd.label_en      AS bundle_label_en,
				   sd.summary       AS bundle_summary,
				   sd.summary_en    AS bundle_summary_en,
				   sda.id           AS fact_a_stance_id,
				   sda.cluster_label AS fact_a_stance_label,
				   sda.label_en      AS fact_a_stance_label_en,
				   sdb.id           AS fact_b_stance_id,
				   sdb.cluster_label AS fact_b_stance_label,
				   sdb.label_en      AS fact_b_stance_label_en,
				   COALESCE(c.bundle_stance_id::text, 'solo:'||c.id::text) AS bundle_key
			FROM contradictions c
			JOIN facts fa ON fa.id = c.fact_a_id
			JOIN facts fb ON fb.id = c.fact_b_id
			LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
			LEFT JOIN stance_document_facts sdfa ON sdfa.fact_id = c.fact_a_id
			LEFT JOIN stance_documents sda ON sda.id = sdfa.stance_document_id
			LEFT JOIN stance_document_facts sdfb ON sdfb.fact_id = c.fact_b_id
			LEFT JOIN stance_documents sdb ON sdb.id = sdfb.stance_document_id
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
		),
		ranked AS (
			SELECT *,
			       COUNT(*) OVER (PARTITION BY bundle_key) AS bundle_size
			FROM contradiction_rows
		)
		SELECT id, person_id, fact_a_id, fact_b_id,
		       fact_a_text, fact_a_text_en, fact_b_text, fact_b_text_en,
		       fact_a_urls, fact_b_urls,
		       fact_a_date, fact_b_date,
		       reason, reason_en, detected_at,
		       bundle_stance_id, bundle_label, bundle_label_en, bundle_summary, bundle_summary_en, bundle_size,
		       fact_a_stance_id, fact_a_stance_label, fact_a_stance_label_en,
		       fact_b_stance_id, fact_b_stance_label, fact_b_stance_label_en,
		       (SELECT COUNT(*) FROM contradiction_rows) AS total_count
		FROM ranked
		ORDER BY bundle_size DESC, bundle_stance_id NULLS LAST, detected_at DESC
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
			&c.FactAText, &c.FactAtextEn, &c.FactBText, &c.FactBtextEn,
			&c.FactASourceURLs, &c.FactBSourceURLs,
			&c.FactADate, &c.FactBDate,
			&c.Reason, &c.ReasonEn, &c.DetectedAt,
			&c.BundleID, &c.BundleLabel, &c.BundleLabelEn, &c.BundleSummary, &c.BundleSummaryEn, &c.BundleSize,
			&c.FactAStanceID, &c.FactAStanceLabel, &c.FactAStanceLabelEn,
			&c.FactBStanceID, &c.FactBStanceLabel, &c.FactBStanceLabelEn,
			&result.Total)
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

		honestyScore := ComputeHonestyScore(factsCount, ContradictionStats{})

		if err := s.updatePersonHonestyScore(ctx, tx, personID, honestyScore, nil, honestyScore); err != nil {
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

// GetFactSourceURLs returns all distinct source URLs registered for a fact,
// ordered by published_at ascending. Empty slice if none are recorded.
func (s *PostgresStorer) GetFactSourceURLs(ctx context.Context, factID int64) ([]string, error) {
	query := `
		SELECT source_url FROM fact_sources
		WHERE fact_id = $1
		ORDER BY published_at ASC NULLS LAST`
	rows, err := s.pool.Query(ctx, query, factID)
	if err != nil {
		return nil, fmt.Errorf("failed to query sources for fact %d: %w", factID, err)
	}
	defer rows.Close()

	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("failed to scan source url: %w", err)
		}
		if u != "" {
			urls = append(urls, u)
		}
	}
	return urls, rows.Err()
}

// GetContradictionByID retrieves a single contradiction enriched with both fact texts,
// source URLs and earliest dates. Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) GetContradictionByID(ctx context.Context, id int64) (*ContradictionWithFacts, error) {
	query := `
		SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
		       fa.fact_text, fb.fact_text,
		       COALESCE(fsa.urls, '{}'), COALESCE(fsb.urls, '{}'),
		       COALESCE(fsa.earliest, fa.created_at), COALESCE(fsb.earliest, fb.created_at),
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
		WHERE c.id = $1`

	var r ContradictionWithFacts
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&r.ID, &r.PersonID, &r.FactAID, &r.FactBID,
		&r.FactAText, &r.FactBText,
		&r.FactASourceURLs, &r.FactBSourceURLs,
		&r.FactADate, &r.FactBDate,
		&r.Reason, &r.DetectedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to get contradiction %d: %w", id, err)
	}
	return &r, nil
}

// DeleteContradiction removes a single contradiction by ID, resets the corresponding
// fact_pair_checks row so the pair can be re-analyzed, and recomputes the owner's
// honesty score. Returns pgx.ErrNoRows if the contradiction does not exist.
func (s *PostgresStorer) DeleteContradiction(ctx context.Context, id int64) error {
	return s.removeContradiction(ctx, id, false)
}

// DismissContradictionAsFalsePositive removes a contradiction the auditor judged
// a false positive. Unlike DeleteContradiction it does NOT clear the pair check —
// it records a NEGATIVE fact_pair_checks row (is_contradiction=false) so the daily
// detector treats the pair as already-judged-not-contradictory and will not
// re-raise it. Without this, an auditor-deleted pair would be re-detected next
// run, re-approved by the (more lenient) insertion verifier, and re-deleted at
// the next audit — thrashing every cycle. Recomputes the owner's honesty score.
// Returns pgx.ErrNoRows if the contradiction does not exist.
func (s *PostgresStorer) DismissContradictionAsFalsePositive(ctx context.Context, id int64) error {
	return s.removeContradiction(ctx, id, true)
}

// removeContradiction is the shared body of DeleteContradiction and
// DismissContradictionAsFalsePositive. suppressReDetection controls the
// fact_pair_checks handling: false clears the row (pair re-analyzed later —
// self-correcting), true upserts a negative row (pair suppressed from future
// detection). Either way the owner's honesty score is recomputed.
func (s *PostgresStorer) removeContradiction(ctx context.Context, id int64, suppressReDetection bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var personID, factAID, factBID int64
	err = tx.QueryRow(ctx,
		`SELECT person_id, fact_a_id, fact_b_id FROM contradictions WHERE id = $1`, id,
	).Scan(&personID, &factAID, &factBID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return pgx.ErrNoRows
		}
		return fmt.Errorf("failed to load contradiction %d: %w", id, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete contradiction %d: %w", id, err)
	}

	if suppressReDetection {
		// Record the pair as judged-not-contradictory so the detector skips it.
		// fact_pair_checks enforces fact_a_id < fact_b_id (canonical ordering).
		a, b := factAID, factBID
		if a > b {
			a, b = b, a
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction)
			 VALUES ($1, $2, $3, false)
			 ON CONFLICT (person_id, fact_a_id, fact_b_id)
			 DO UPDATE SET is_contradiction = false, checked_at = NOW()`,
			personID, a, b); err != nil {
			return fmt.Errorf("failed to record negative pair check: %w", err)
		}
	} else {
		// fact_pair_checks enforces fact_a_id < fact_b_id; check both orderings to be safe.
		if _, err := tx.Exec(ctx,
			`DELETE FROM fact_pair_checks
			 WHERE person_id = $1
			   AND ((fact_a_id = $2 AND fact_b_id = $3) OR (fact_a_id = $3 AND fact_b_id = $2))`,
			personID, factAID, factBID); err != nil {
			return fmt.Errorf("failed to reset pair check: %w", err)
		}
	}

	factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get facts count: %w", err)
	}
	stats, err := s.getPersonContradictionStats(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to compute contradiction stats: %w", err)
	}
	ownScore := ComputeHonestyScore(factsCount, stats)

	var entityType *string
	if err := tx.QueryRow(ctx, `SELECT entity_type FROM persons WHERE id = $1`, personID).Scan(&entityType); err != nil {
		return fmt.Errorf("failed to get entity_type for person %d: %w", personID, err)
	}
	var membersScore *float64
	if entityType != nil && *entityType != "human" {
		membersScore, err = s.computeMembersScore(ctx, tx, personID)
		if err != nil {
			return fmt.Errorf("failed to compute members score: %w", err)
		}
	}
	aggregate := ComputeAggregateScore(ownScore, membersScore)

	if err := s.updatePersonHonestyScore(ctx, tx, personID, ownScore, membersScore, aggregate); err != nil {
		return fmt.Errorf("failed to update honesty score: %w", err)
	}

	payload := map[string]interface{}{
		"honesty_score":          aggregate,
		"contradiction_id":       id,
		"contradictions_deleted": 1,
	}
	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := s.recomputeParentEntityScores(ctx, tx, personID); err != nil {
		return fmt.Errorf("failed to recompute parent entity scores: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit deletion: %w", err)
	}

	log.Info().
		Int64("contradiction_id", id).
		Int64("person_id", personID).
		Float64("honesty_score", aggregate).
		Bool("suppressed", suppressReDetection).
		Msg("Deleted single contradiction")
	return nil
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

	// Reset honesty score to 1.0 and fact_count to 0
	if err := s.updatePersonHonestyScore(ctx, tx, personID, 1.0, nil, 1.0); err != nil {
		return 0, fmt.Errorf("failed to reset honesty score: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE persons SET fact_count = 0 WHERE id = $1`, personID); err != nil {
		return 0, fmt.Errorf("failed to reset fact_count: %w", err)
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

// FactAuditRow is one fact returned by ListFactsForAudit. Persons mentioning
// the fact are aggregated into Persons (a fact may belong to several people via
// fact_mentions). FirstSourceURL / FirstOriginalQuote are picked from the
// earliest-published source so the auditor sees a representative snippet.
type FactAuditRow struct {
	ID                 int64
	FactText           string
	EmbeddingText      string
	CanonicalVerb      string
	Persons            []FactAuditPerson
	FirstSourceURL     string
	FirstOriginalQuote string
	// FirstSourcePublishedAt is the earliest non-null published_at across the
	// fact's sources; nil if none of the sources carry a publication date.
	FirstSourcePublishedAt *time.Time
	// FactCreatedAt is when we ingested this fact. Always set.
	FactCreatedAt time.Time
}

// FactAuditPerson is the small shape returned alongside a FactAuditRow.
type FactAuditPerson struct {
	ID   int64
	Name string
}

// ListFactsForAudit returns facts for the auditor, optionally filtered to a
// single person and/or to a specific fact id. Order is newest-first by
// default; pass random=true for a uniform sample. limit=0 means no cap.
func (s *PostgresStorer) ListFactsForAudit(ctx context.Context, personID *int64, factID *int64, random bool, limit int) ([]FactAuditRow, error) {
	var (
		conds []string
		args  []any
	)
	if personID != nil {
		args = append(args, *personID)
		conds = append(conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM fact_sources fs
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fs.fact_id = f.id AND fm.person_id = $%d
		)`, len(args)))
	}
	if factID != nil {
		args = append(args, *factID)
		conds = append(conds, fmt.Sprintf("f.id = $%d", len(args)))
	}
	whereClause := ""
	if len(conds) > 0 {
		whereClause = "WHERE " + strings.Join(conds, " AND ")
	}
	var orderClause string
	switch {
	case factID != nil:
		// Single-fact lookup: ordering is meaningless.
		orderClause = ""
	case random:
		orderClause = "ORDER BY RANDOM()"
	default:
		orderClause = "ORDER BY f.created_at DESC"
	}
	if limit > 0 {
		args = append(args, limit)
		orderClause += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	query := fmt.Sprintf(`
		SELECT
			f.id,
			f.fact_text,
			COALESCE(f.embedding_text, ''),
			COALESCE(f.canonical_verb, ''),
			f.created_at,
			COALESCE(first_src.source_url, '')      AS first_source_url,
			COALESCE(first_src.original_quote, '')  AS first_original_quote,
			first_src.published_at                  AS first_source_published_at,
			COALESCE(persons_agg.persons_json, '[]'::jsonb) AS persons
		FROM facts f
		LEFT JOIN LATERAL (
			SELECT fs.source_url, fs.original_quote, fs.published_at
			FROM fact_sources fs
			WHERE fs.fact_id = f.id
			ORDER BY fs.published_at ASC NULLS LAST, fs.id ASC
			LIMIT 1
		) first_src ON true
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(jsonb_build_object('id', p.id, 'name', p.name) ORDER BY p.name) AS persons_json
			FROM (
				SELECT DISTINCT fm.person_id
				FROM fact_sources fs
				JOIN fact_mentions fm ON fm.fact_source_id = fs.id
				WHERE fs.fact_id = f.id
			) ids
			JOIN persons p ON p.id = ids.person_id
		) persons_agg ON true
		%s
		%s`, whereClause, orderClause)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for audit: %w", err)
	}
	defer rows.Close()

	out := make([]FactAuditRow, 0)
	for rows.Next() {
		var r FactAuditRow
		var personsJSON []byte
		if err := rows.Scan(
			&r.ID,
			&r.FactText,
			&r.EmbeddingText,
			&r.CanonicalVerb,
			&r.FactCreatedAt,
			&r.FirstSourceURL,
			&r.FirstOriginalQuote,
			&r.FirstSourcePublishedAt,
			&personsJSON,
		); err != nil {
			return nil, fmt.Errorf("failed to scan fact audit row: %w", err)
		}
		if len(personsJSON) > 0 {
			if err := json.Unmarshal(personsJSON, &r.Persons); err != nil {
				return nil, fmt.Errorf("failed to decode persons array for fact %d: %w", r.ID, err)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating facts for audit: %w", err)
	}
	return out, nil
}

// FactDeletionCleanupResult summarises what DeleteFactsAndCleanup did.
type FactDeletionCleanupResult struct {
	FactsDeleted    int     // rows removed from facts
	StancesDeleted  int     // stance_documents that became empty after the cascade and were dropped
	PersonsAffected []int64 // persons whose honesty score was recomputed
}

// DeleteFactsAndCleanup removes a set of facts and cascades the cleanup the
// schema's ON DELETE CASCADE rules already partially handle (contradictions,
// stance_document_facts, fact_sources, fact_mentions, fact_relations,
// fact_pair_checks). It then:
//
//   - drops any stance_documents that became empty after the cascade (so we
//     don't leave dangling label/summary rows whose centroid no longer
//     describes anything),
//   - recomputes the honesty score for every person mentioned by any of the
//     deleted facts (the cascade dropped contradictions and shifted bundle
//     counts, so the score is stale otherwise).
//
// All work happens in a single transaction.
func (s *PostgresStorer) DeleteFactsAndCleanup(ctx context.Context, factIDs []int64) (FactDeletionCleanupResult, error) {
	var res FactDeletionCleanupResult
	if len(factIDs) == 0 {
		return res, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Snapshot affected persons and stances BEFORE the cascade fires, since
	//    the joins disappear when the rows are deleted.
	affectedPersons, err := scanInt64Set(ctx, tx, `
		SELECT DISTINCT fm.person_id
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fs.fact_id = ANY($1)`, factIDs)
	if err != nil {
		return res, fmt.Errorf("failed to collect affected persons: %w", err)
	}

	affectedStances, err := scanInt64Set(ctx, tx, `
		SELECT DISTINCT stance_document_id
		FROM stance_document_facts
		WHERE fact_id = ANY($1)`, factIDs)
	if err != nil {
		return res, fmt.Errorf("failed to collect affected stances: %w", err)
	}

	// 2. Delete the facts. CASCADE handles:
	//      - contradictions (fact_a_id / fact_b_id REFERENCES facts ON DELETE CASCADE)
	//      - stance_document_facts (fact_id REFERENCES facts ON DELETE CASCADE)
	//      - fact_sources -> fact_mentions (CASCADE chain)
	//      - fact_relations, fact_pair_checks (CASCADE)
	tag, err := tx.Exec(ctx, `DELETE FROM facts WHERE id = ANY($1)`, factIDs)
	if err != nil {
		return res, fmt.Errorf("failed to delete facts: %w", err)
	}
	res.FactsDeleted = int(tag.RowsAffected())

	// 3. Drop stance_documents that became empty as a side-effect of the cascade.
	//    A stance with zero facts has no meaningful label/summary/centroid and
	//    will pollute future stance routing if left in place.
	if len(affectedStances) > 0 {
		tag, err := tx.Exec(ctx, `
			DELETE FROM stance_documents
			WHERE id = ANY($1)
			  AND NOT EXISTS (
				SELECT 1 FROM stance_document_facts sdf WHERE sdf.stance_document_id = stance_documents.id
			  )`, affectedStances)
		if err != nil {
			return res, fmt.Errorf("failed to delete now-empty stances: %w", err)
		}
		res.StancesDeleted = int(tag.RowsAffected())
	}

	// 4. Recompute honesty score for each affected person. The cascade may have
	//    deleted contradictions, so bundle counts and the absolute-decay term
	//    have shifted. We emit a person_updated event but NOT a contradiction
	//    event — none was added.
	for _, pid := range affectedPersons {
		if err := s.recomputeScoreAfterFactDeletion(ctx, tx, pid, res.FactsDeleted); err != nil {
			return res, fmt.Errorf("failed to recompute honesty score for person %d: %w", pid, err)
		}
	}
	res.PersonsAffected = affectedPersons

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("failed to commit fact deletion: %w", err)
	}

	log.Info().
		Int("facts_deleted", res.FactsDeleted).
		Int("stances_deleted", res.StancesDeleted).
		Int("persons_recomputed", len(res.PersonsAffected)).
		Msg("DeleteFactsAndCleanup complete")
	return res, nil
}

// recomputeScoreAfterFactDeletion is the equivalent of
// recomputeHonestyScoreAndEmitEvents but tailored for the post-delete path:
// no contradiction-specific event payload, since deletion did not add one.
// The parent-entity rollup is still run so non-human entities reflect the
// updated member scores.
func (s *PostgresStorer) recomputeScoreAfterFactDeletion(ctx context.Context, tx pgx.Tx, personID int64, factsDeletedTotal int) error {
	factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
	if err != nil {
		return err
	}
	stats, err := s.getPersonContradictionStats(ctx, tx, personID)
	if err != nil {
		return err
	}
	ownScore := ComputeHonestyScore(factsCount, stats)

	var entityType *string
	if err := tx.QueryRow(ctx, `SELECT entity_type FROM persons WHERE id = $1`, personID).Scan(&entityType); err != nil {
		return fmt.Errorf("failed to get entity_type: %w", err)
	}

	var membersScore *float64
	if entityType != nil && *entityType != "human" {
		membersScore, err = s.computeMembersScore(ctx, tx, personID)
		if err != nil {
			return err
		}
	}
	aggregate := ComputeAggregateScore(ownScore, membersScore)

	if err := s.updatePersonHonestyScore(ctx, tx, personID, ownScore, membersScore, aggregate); err != nil {
		return err
	}

	if err := s.refreshPersonFactCount(ctx, tx, personID); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"honesty_score":  aggregate,
		"own_score":      ownScore,
		"members_score":  membersScore,
		"facts_count":    factsCount,
		"contradictions": stats.Total,
		"bundles":        stats.Bundles,
		"reason":         "fact_audit_deletion",
	}
	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := s.recomputeParentEntityScores(ctx, tx, personID); err != nil {
		return fmt.Errorf("failed to recompute parent entity scores: %w", err)
	}

	log.Info().
		Int64("person_id", personID).
		Float64("own_score", ownScore).
		Float64("aggregate", aggregate).
		Int("facts_count", factsCount).
		Int("bundles", stats.Bundles).
		Int("contradictions_total", stats.Total).
		Msg("Recomputed score after fact deletion")
	return nil
}

func scanInt64Set(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
			p.id, p.name, p.entity_type, p.honesty_score, p.own_score, p.members_score, p.image_url
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
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.OwnScore, &person.MembersScore, &person.ImageURL)
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
		Str("new_text", normalizedFact[:min(80, len(normalizedFact))]).
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

// --- Audit / Attribution Cleanup Helpers ---

// FactSourceAuditRow is a minimal projection of a fact_sources row, sufficient
// for the `facts audit-attribution` one-shot cleanup. Returned by IterateFactSources.
type FactSourceAuditRow struct {
	ID            int64
	FactID        int64
	SourceURL     string
	OriginalQuote string
}

// IterateFactSources returns up to `limit` fact_sources rows with id > afterID,
// ordered by id. Use keyset pagination via afterID for resumable streaming.
func (s *PostgresStorer) IterateFactSources(ctx context.Context, afterID int64, limit int) ([]FactSourceAuditRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fs.id, fs.fact_id, fs.source_url, COALESCE(fs.original_quote, '')
		FROM fact_sources fs
		WHERE fs.id > $1
		ORDER BY fs.id
		LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("IterateFactSources query failed: %w", err)
	}
	defer rows.Close()
	var out []FactSourceAuditRow
	for rows.Next() {
		var r FactSourceAuditRow
		if err := rows.Scan(&r.ID, &r.FactID, &r.SourceURL, &r.OriginalQuote); err != nil {
			return nil, fmt.Errorf("IterateFactSources scan failed: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteFactSourceByID removes a single fact_sources row. The FK CASCADE on
// fact_mentions(fact_source_id) handles its mention rows automatically. The
// parent fact may end up orphaned (zero remaining sources) — call
// FindOrphanFacts + DeleteFactsAndCleanup afterward to sweep those up.
func (s *PostgresStorer) DeleteFactSourceByID(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM fact_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeleteFactSourceByID(%d): %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("DeleteFactSourceByID(%d): no row deleted", id)
	}
	return nil
}

// FindOrphanFacts returns the subset of factIDs that have zero remaining
// fact_sources rows. The result is the input list for DeleteFactsAndCleanup.
func (s *PostgresStorer) FindOrphanFacts(ctx context.Context, factIDs []int64) ([]int64, error) {
	if len(factIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT f.id
		FROM facts f
		WHERE f.id = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM fact_sources fs WHERE fs.fact_id = f.id)`, factIDs)
	if err != nil {
		return nil, fmt.Errorf("FindOrphanFacts query failed: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("FindOrphanFacts scan failed: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FactSourceURLStats is a quick diagnostic projection for sizing the per-URL
// cleanup run.
type FactSourceURLStats struct {
	TotalRows  int64
	UniqueURLs int64
}

// FactSourceURLStats returns row and distinct-URL counts for fact_sources.
func (s *PostgresStorer) FactSourceURLStats(ctx context.Context) (FactSourceURLStats, error) {
	var out FactSourceURLStats
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*), COUNT(DISTINCT source_url) FROM fact_sources`).Scan(&out.TotalRows, &out.UniqueURLs); err != nil {
		return out, fmt.Errorf("FactSourceURLStats: %w", err)
	}
	return out, nil
}

// IterateUniqueFactSourceURLs returns up to `limit` distinct source_urls from
// fact_sources where source_url > afterURL (keyset pagination by string).
func (s *PostgresStorer) IterateUniqueFactSourceURLs(ctx context.Context, afterURL string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT source_url
		FROM fact_sources
		WHERE source_url > $1
		ORDER BY source_url
		LIMIT $2`, afterURL, limit)
	if err != nil {
		return nil, fmt.Errorf("IterateUniqueFactSourceURLs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteFactSourcesByURL deletes every fact_sources row matching the given URL
// and source_type, returning the deleted fact_ids (for the caller to feed into
// FindOrphanFacts + DeleteFactsAndCleanup). Cascades fact_mentions automatically.
// If sourceType is empty, deletes across all source_types — only do that when you
// want to wipe the URL completely.
func (s *PostgresStorer) DeleteFactSourcesByURL(ctx context.Context, url, sourceType string) ([]int64, error) {
	var rows pgx.Rows
	var err error
	if sourceType == "" {
		rows, err = s.pool.Query(ctx, `DELETE FROM fact_sources WHERE source_url = $1 RETURNING fact_id`, url)
	} else {
		rows, err = s.pool.Query(ctx, `DELETE FROM fact_sources WHERE source_url = $1 AND source_type = $2 RETURNING fact_id`, url, sourceType)
	}
	if err != nil {
		return nil, fmt.Errorf("DeleteFactSourcesByURL: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// NewsURLsForPerson returns the distinct news source_urls that have at least one
// fact_mention for the given person_id. Used by the rebuild-from-urls path to
// know which URLs to re-extract for each tracked person.
func (s *PostgresStorer) NewsURLsForPerson(ctx context.Context, personID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT fs.source_url
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1 AND fs.source_type = 'news'
		ORDER BY fs.source_url`, personID)
	if err != nil {
		return nil, fmt.Errorf("NewsURLsForPerson: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- Fact Relations Methods ---

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

// MarkTwitterSearched sets twitter_searched_at to now for a person,
// so that discovery won't re-search them until the cooldown expires.
func (s *PostgresStorer) MarkTwitterSearched(ctx context.Context, personID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE persons SET twitter_searched_at = NOW() WHERE id = $1`, personID)
	if err != nil {
		return fmt.Errorf("failed to mark twitter searched for person %d: %w", personID, err)
	}
	return nil
}

// GetTwitterSearchedAt returns the last time Twitter discovery was attempted for a person.
func (s *PostgresStorer) GetTwitterSearchedAt(ctx context.Context, personID int64) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT twitter_searched_at FROM persons WHERE id = $1`, personID).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("failed to get twitter_searched_at for person %d: %w", personID, err)
	}
	return t, nil
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
		SELECT id, name, entity_type, honesty_score, own_score, members_score, image_url
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
		err := rows.Scan(&person.ID, &person.Name, &person.EntityType, &person.HonestyScore, &person.OwnScore, &person.MembersScore, &person.ImageURL)
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

// GetLatestJobRunTime returns the timestamp of the most recent job_metrics
// row for the given job_type. Returns (nil, nil) when the job has never run.
// Used by cadence-gated pipeline steps (e.g. the contradiction auditor, which
// runs every N days rather than every daily-pipeline tick).
func (s *PostgresStorer) GetLatestJobRunTime(ctx context.Context, jobType string) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(created_at) FROM job_metrics WHERE job_type = $1`, jobType).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("get latest job run time for %q: %w", jobType, err)
	}
	return t, nil
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

// PipelineRunSummary is one daily-pipeline run's outcome. The scalar fields are
// supplied by the caller (cmd/pipeline.go); facts_extracted, contradictions_found
// and persons_updated are derived by RecordPipelineRun from the run window so the
// caller does not have to thread counts through every step.
type PipelineRunSummary struct {
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       string // "success" | "warning" | "no_content" | "error"
	Targets      int
	NewsArticles int
	ScrapeErrors int
	AuditChecked int
	AuditFlagged int
	StepErrors   int
	ErrorSummary string
}

// RecordPipelineRun persists one pipeline_runs row. facts_extracted,
// contradictions_found and persons_updated are counted from the [StartedAt,
// FinishedAt] window in the same statement (single round trip) rather than
// passed in. Best-effort: callers should log a failure but not abort the run.
func (s *PostgresStorer) RecordPipelineRun(ctx context.Context, sum PipelineRunSummary) error {
	var errSummary *string
	if sum.ErrorSummary != "" {
		errSummary = &sum.ErrorSummary
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO pipeline_runs (
			started_at, finished_at, duration_seconds, status,
			targets, news_articles, scrape_errors,
			facts_extracted, contradictions_found, persons_updated,
			audit_checked, audit_flagged, step_errors, error_summary
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			(SELECT count(*) FROM facts WHERE created_at >= $1 AND created_at <= $2),
			(SELECT count(*) FROM contradictions WHERE created_at >= $1 AND created_at <= $2),
			(SELECT count(DISTINCT entity_id) FROM event_log WHERE topic = 'person_updated' AND created_at >= $1 AND created_at <= $2),
			$8, $9, $10, $11
		)`,
		sum.StartedAt, sum.FinishedAt, int(sum.FinishedAt.Sub(sum.StartedAt).Seconds()), sum.Status,
		sum.Targets, sum.NewsArticles, sum.ScrapeErrors,
		sum.AuditChecked, sum.AuditFlagged, sum.StepErrors, errSummary,
	)
	if err != nil {
		return fmt.Errorf("record pipeline run: %w", err)
	}
	return nil
}

// FactNeedingTranslation is a non-English fact whose English translation is
// missing (fact_text_en IS NULL), returned by GetFactsMissingEnglish.
type FactNeedingTranslation struct {
	ID   int64
	Text string
	Lang string
}

// GetFactsMissingEnglish returns up to `limit` non-English facts with a NULL
// fact_text_en, oldest first. These are the gaps the daily extraction left
// behind (the model dropped the translation field); the repair pass fills them.
func (s *PostgresStorer) GetFactsMissingEnglish(ctx context.Context, limit int) ([]FactNeedingTranslation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, fact_text, lang
		 FROM facts
		 WHERE lang IS NOT NULL AND lang <> 'en'
		   AND (fact_text_en IS NULL OR fact_text_en = '')
		 ORDER BY id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query facts missing english: %w", err)
	}
	defer rows.Close()

	var out []FactNeedingTranslation
	for rows.Next() {
		var f FactNeedingTranslation
		if err := rows.Scan(&f.ID, &f.Text, &f.Lang); err != nil {
			return nil, fmt.Errorf("scan fact missing english: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkFactDuplicate records that two facts are duplicates by inserting a
// 'duplicate' fact_relations row (keeping the lower id as the canonical/general
// fact, hiding the higher id as the specific one — it is then excluded from
// active-fact queries, dedup candidates, and future contradiction detection).
// Non-destructive and reversible (delete the row to un-hide). Idempotent via the
// uq_fact_relation constraint. Used by the contradiction-detection dedup net.
func (s *PostgresStorer) MarkFactDuplicate(ctx context.Context, factID1, factID2 int64, reason string) error {
	if factID1 == factID2 {
		return fmt.Errorf("cannot mark a fact as a duplicate of itself: %d", factID1)
	}
	general, specific := factID1, factID2
	if general > specific {
		general, specific = specific, general
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		 VALUES ($1, $2, 'duplicate', NULLIF($3, ''))
		 ON CONFLICT (general_fact_id, specific_fact_id) DO NOTHING`,
		general, specific, reason)
	if err != nil {
		return fmt.Errorf("mark fact %d duplicate of %d: %w", specific, general, err)
	}
	return nil
}

// FactDuplicatePair is a (canonical, duplicate) fact pair from a 'duplicate'
// fact_relations row whose duplicate side still anchors live contradictions.
type FactDuplicatePair struct {
	General  int64 // canonical fact kept
	Specific int64 // duplicate fact (hidden); its contradictions need repointing
}

// GetDuplicatesNeedingRepoint returns 'duplicate' relations whose specific
// (hidden) fact is still referenced by at least one contradiction — the only
// ones RepointFactDuplicate has work to do on.
func (s *PostgresStorer) GetDuplicatesNeedingRepoint(ctx context.Context) ([]FactDuplicatePair, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT fr.general_fact_id, fr.specific_fact_id
		 FROM fact_relations fr
		 WHERE fr.relation_type = 'duplicate'
		   AND EXISTS (SELECT 1 FROM contradictions c
		               WHERE c.fact_a_id = fr.specific_fact_id OR c.fact_b_id = fr.specific_fact_id)`)
	if err != nil {
		return nil, fmt.Errorf("query duplicates needing repoint: %w", err)
	}
	defer rows.Close()
	var out []FactDuplicatePair
	for rows.Next() {
		var p FactDuplicatePair
		if err := rows.Scan(&p.General, &p.Specific); err != nil {
			return nil, fmt.Errorf("scan duplicate pair: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RepointFactDuplicate moves contradictions that reference the `specific`
// (hidden duplicate) fact onto the `general` (canonical) fact, then recomputes
// affected persons' honesty scores. For each such contradiction {specific, X}:
//   - X == general  → it is a fact-vs-its-own-duplicate self-pair: delete it.
//   - {general, X} already recorded for that person → the repointed pair would
//     duplicate it: delete the {specific, X} row.
//   - otherwise → repoint the row to {general, X} (ordered, fact_a_id<fact_b_id).
//
// Stance memberships of `specific` are intentionally left untouched: the fact is
// already hidden from active queries, and keeping its stance row preserves the
// every-fact-has-a-stance invariant. Returns (repointed, dropped).
func (s *PostgresStorer) RepointFactDuplicate(ctx context.Context, general, specific int64) (repointed int, dropped int, err error) {
	if general == specific {
		return 0, 0, fmt.Errorf("general and specific are the same fact: %d", general)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin repoint tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id, person_id, fact_a_id, fact_b_id FROM contradictions
		 WHERE fact_a_id = $1 OR fact_b_id = $1`, specific)
	if err != nil {
		return 0, 0, fmt.Errorf("load contradictions for fact %d: %w", specific, err)
	}
	type crow struct{ id, person, a, b int64 }
	var crows []crow
	for rows.Next() {
		var c crow
		if err := rows.Scan(&c.id, &c.person, &c.a, &c.b); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan contradiction: %w", err)
		}
		crows = append(crows, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate contradictions: %w", err)
	}

	affected := make(map[int64]struct{})
	for _, c := range crows {
		other := c.a
		if c.a == specific {
			other = c.b
		}
		affected[c.person] = struct{}{}

		if other == general {
			if _, e := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, c.id); e != nil {
				return 0, 0, fmt.Errorf("delete self-pair contradiction %d: %w", c.id, e)
			}
			dropped++
			continue
		}

		na, nb := general, other
		if na > nb {
			na, nb = nb, na
		}
		var exists bool
		if e := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM contradictions WHERE person_id=$1 AND fact_a_id=$2 AND fact_b_id=$3 AND id<>$4)`,
			c.person, na, nb, c.id).Scan(&exists); e != nil {
			return 0, 0, fmt.Errorf("check existing contradiction: %w", e)
		}
		if exists {
			if _, e := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, c.id); e != nil {
				return 0, 0, fmt.Errorf("delete redundant contradiction %d: %w", c.id, e)
			}
			dropped++
		} else {
			if _, e := tx.Exec(ctx,
				`UPDATE contradictions SET fact_a_id=$2, fact_b_id=$3 WHERE id=$1`, c.id, na, nb); e != nil {
				return 0, 0, fmt.Errorf("repoint contradiction %d: %w", c.id, e)
			}
			repointed++
		}
	}

	for p := range affected {
		if e := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, p, 0); e != nil {
			return 0, 0, fmt.Errorf("recompute score for person %d: %w", p, e)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit repoint: %w", err)
	}
	return repointed, dropped, nil
}

// SetFactEnglish writes the English translation for a single fact.
func (s *PostgresStorer) SetFactEnglish(ctx context.Context, factID int64, english string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE facts SET fact_text_en = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
		factID, english)
	if err != nil {
		return fmt.Errorf("set fact english for %d: %w", factID, err)
	}
	return nil
}

// ScoreUpdate holds old and new honesty scores for a person, used by RecomputeAllHonestyScores.
// Bundles is the count of distinct stance-grouped contradiction bundles; Total is the raw pair
// count (Bundles <= Total always).
type ScoreUpdate struct {
	PersonID     int64
	PersonName   string
	EntityType   *string
	OldScore     float64
	NewScore     float64
	OwnScore     float64
	MembersScore *float64
	Facts        int
	Bundles      int
	Total        int
}

// RecomputeAllHonestyScores recalculates honesty scores for every person
// using ComputeHonestyScore. If dryRun is true, scores are not written.
// Two-pass approach: pass 1 computes own_score for all, pass 2 computes members_score + aggregate for non-human entities.
func (s *PostgresStorer) RecomputeAllHonestyScores(ctx context.Context, dryRun bool) ([]ScoreUpdate, error) {
	query := `
		SELECT p.id, p.name, p.entity_type, p.honesty_score,
		       COALESCE(fc.cnt, 0)     AS facts_count,
		       COALESCE(cc.bundles, 0) AS bundles,
		       COALESCE(cc.total, 0)   AS total
		FROM persons p
		LEFT JOIN (
			SELECT fm.person_id, COUNT(DISTINCT f.id) AS cnt
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			GROUP BY fm.person_id
		) fc ON fc.person_id = p.id
		LEFT JOIN (
			SELECT person_id,
			       COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:'||id::text)) AS bundles,
			       COUNT(*) AS total
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
		if err := rows.Scan(&u.PersonID, &u.PersonName, &u.EntityType, &u.OldScore, &u.Facts, &u.Bundles, &u.Total); err != nil {
			return nil, fmt.Errorf("failed to scan person row: %w", err)
		}
		u.OwnScore = ComputeHonestyScore(u.Facts, ContradictionStats{Bundles: u.Bundles, Total: u.Total})
		u.NewScore = u.OwnScore // default for humans
		updates = append(updates, u)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	if !dryRun {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Pass 1: write all own_scores so members_score queries can use them
		for _, u := range updates {
			if _, err := tx.Exec(ctx, `UPDATE persons SET own_score = $1, honesty_score = $2 WHERE id = $3`, u.OwnScore, u.OwnScore, u.PersonID); err != nil {
				return nil, fmt.Errorf("failed to update own_score for person %d: %w", u.PersonID, err)
			}
		}

		// Pass 2: compute members_score for non-human entities
		for i, u := range updates {
			if u.EntityType != nil && *u.EntityType != "human" {
				membersScore, err := s.computeMembersScore(ctx, tx, u.PersonID)
				if err != nil {
					return nil, fmt.Errorf("failed to compute members score for entity %d: %w", u.PersonID, err)
				}
				updates[i].MembersScore = membersScore
				updates[i].NewScore = ComputeAggregateScore(u.OwnScore, membersScore)

				if _, err := tx.Exec(ctx, `UPDATE persons SET members_score = $1, honesty_score = $2 WHERE id = $3`, membersScore, updates[i].NewScore, u.PersonID); err != nil {
					return nil, fmt.Errorf("failed to update aggregate score for entity %d: %w", u.PersonID, err)
				}
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("failed to commit score updates: %w", err)
		}
	} else {
		// Dry run: compute members_score for display based on current DB state
		for i, u := range updates {
			if u.EntityType != nil && *u.EntityType != "human" {
				var avg *float64
				err := s.pool.QueryRow(ctx, `
					SELECT AVG(p.honesty_score) FROM persons p
					JOIN person_links pl ON p.id = pl.src_person_id
					WHERE pl.dst_person_id = $1
					  AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)`, u.PersonID).Scan(&avg)
				if err != nil {
					return nil, fmt.Errorf("failed to compute members score for entity %d: %w", u.PersonID, err)
				}
				updates[i].MembersScore = avg
				updates[i].NewScore = ComputeAggregateScore(u.OwnScore, avg)
			}
		}
	}

	return updates, nil
}

// --- Stance Document Storage ---

// StanceDocument represents a stored stance document.
type StanceDocument struct {
	ID               int64
	PersonID         int64
	ClusterLabel     string
	LabelEn          string // English translation of ClusterLabel; empty if already English / not translated yet
	ClusterEmbedding []float32
	Summary          string
	SummaryEn        string // English translation of Summary; empty if already English / not translated yet
	KeyFactIDs       []int64
	FactCount        int
	LastUpdatedAt    time.Time
	CreatedAt        time.Time
}

// StanceMatch represents a stance document found via embedding similarity search.
type StanceMatch struct {
	StanceDocument
	Distance float64
}

// StanceFactInfo holds basic info about a fact associated with a stance document.
type StanceFactInfo struct {
	ID                 int64
	NormalizedFactText string
	EmbeddingText      string
	CanonicalVerb      string
	Lang               string // BCP-47 source language; always set on new inserts.
}

// FindMatchingStance finds the closest stance document for a person within a distance threshold.
// Returns nil, nil if no matching stance is found.
func (s *PostgresStorer) FindMatchingStance(ctx context.Context, personID int64, embedding []float32, threshold float64) (*StanceMatch, error) {
	query := `
		SELECT id, person_id, cluster_label, cluster_embedding, summary, key_fact_ids, fact_count,
		       last_updated_at, created_at, cluster_embedding <-> $2 AS distance
		FROM stance_documents
		WHERE person_id = $1
		  AND cluster_embedding <-> $2 <= $3
		ORDER BY cluster_embedding <-> $2
		LIMIT 1`

	var match StanceMatch
	var vec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, personID, pgvector.NewVector(embedding), threshold).Scan(
		&match.ID,
		&match.PersonID,
		&match.ClusterLabel,
		&vec,
		&match.Summary,
		&match.KeyFactIDs,
		&match.FactCount,
		&match.LastUpdatedAt,
		&match.CreatedAt,
		&match.Distance,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("finding matching stance for person %d: %w", personID, err)
	}

	match.ClusterEmbedding = vec.Slice()
	return &match, nil
}

// InsertStanceDocument inserts a new stance document and returns its ID.
func (s *PostgresStorer) InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error) {
	query := `
		INSERT INTO stance_documents (person_id, cluster_label, label_en, cluster_embedding, summary, summary_en, key_fact_ids, fact_count, last_updated_at)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, NULLIF($6,''), $7, $8, NOW())
		RETURNING id`

	var id int64
	err := s.pool.QueryRow(ctx, query,
		doc.PersonID,
		doc.ClusterLabel,
		doc.LabelEn,
		pgvector.NewVector(doc.ClusterEmbedding),
		doc.Summary,
		doc.SummaryEn,
		doc.KeyFactIDs,
		doc.FactCount,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting stance document: %w", err)
	}

	return id, nil
}

// UpdateStanceDocument updates an existing stance document's summary, embedding, key facts, and count.
func (s *PostgresStorer) UpdateStanceDocument(ctx context.Context, id int64, summary string, summaryEn string, embedding []float32, keyFactIDs []int64, factCount int) error {
	query := `
		UPDATE stance_documents
		SET summary = $1, summary_en = NULLIF($2,''), cluster_embedding = $3, key_fact_ids = $4, fact_count = $5, last_updated_at = NOW()
		WHERE id = $6`

	_, err := s.pool.Exec(ctx, query, summary, summaryEn, pgvector.NewVector(embedding), keyFactIDs, factCount, id)
	if err != nil {
		return fmt.Errorf("updating stance document %d: %w", id, err)
	}

	return nil
}

// UpdateStanceMetadata updates only the cluster_label/label_en and summary/summary_en of a stance.
// Used by the per-fact stance refresh path (StanceManager.triggerStanceRefresh)
// to keep the stance's title and description in sync with its accumulating
// fact set without touching the centroid / fact_count / key_fact_ids.
func (s *PostgresStorer) UpdateStanceMetadata(ctx context.Context, id int64, label, labelEn, summary, summaryEn string) error {
	query := `
		UPDATE stance_documents
		SET cluster_label = $1, label_en = NULLIF($2,''), summary = $3, summary_en = NULLIF($4,''), last_updated_at = NOW()
		WHERE id = $5`

	_, err := s.pool.Exec(ctx, query, label, labelEn, summary, summaryEn, id)
	if err != nil {
		return fmt.Errorf("updating stance metadata %d: %w", id, err)
	}
	return nil
}

// AddFactToStance adds a fact to the stance document join table. No-op if already linked.
func (s *PostgresStorer) AddFactToStance(ctx context.Context, stanceID int64, factID int64) error {
	query := `INSERT INTO stance_document_facts (stance_document_id, fact_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`

	_, err := s.pool.Exec(ctx, query, stanceID, factID)
	if err != nil {
		return fmt.Errorf("adding fact %d to stance %d: %w", factID, stanceID, err)
	}

	return nil
}

// GetStanceDocumentsForPerson lists all stance documents for a person, ordered by fact count descending.
func (s *PostgresStorer) GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error) {
	query := `
		SELECT id, person_id, cluster_label, COALESCE(label_en,'') AS label_en, cluster_embedding,
		       summary, COALESCE(summary_en,'') AS summary_en, key_fact_ids, fact_count, last_updated_at, created_at
		FROM stance_documents
		WHERE person_id = $1
		ORDER BY fact_count DESC`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("querying stance documents for person %d: %w", personID, err)
	}
	defer rows.Close()

	var docs []StanceDocument
	for rows.Next() {
		var doc StanceDocument
		var vec pgvector.Vector
		if err := rows.Scan(
			&doc.ID,
			&doc.PersonID,
			&doc.ClusterLabel,
			&doc.LabelEn,
			&vec,
			&doc.Summary,
			&doc.SummaryEn,
			&doc.KeyFactIDs,
			&doc.FactCount,
			&doc.LastUpdatedAt,
			&doc.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning stance document: %w", err)
		}
		doc.ClusterEmbedding = vec.Slice()
		docs = append(docs, doc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stance documents for person %d: %w", personID, err)
	}

	return docs, nil
}

// GetFactsForStance retrieves all facts associated with a stance document.
func (s *PostgresStorer) GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), COALESCE(f.lang, '')
		FROM facts f
		JOIN stance_document_facts sdf ON f.id = sdf.fact_id
		WHERE sdf.stance_document_id = $1
		ORDER BY f.id`

	rows, err := s.pool.Query(ctx, query, stanceID)
	if err != nil {
		return nil, fmt.Errorf("querying facts for stance %d: %w", stanceID, err)
	}
	defer rows.Close()

	var facts []StanceFactInfo
	for rows.Next() {
		var f StanceFactInfo
		if err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText, &f.CanonicalVerb, &f.Lang); err != nil {
			return nil, fmt.Errorf("scanning stance fact: %w", err)
		}
		facts = append(facts, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating facts for stance %d: %w", stanceID, err)
	}

	return facts, nil
}

// GetStanceDocumentCounts returns aggregate stats: total stance documents and total facts linked.
func (s *PostgresStorer) GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error) {
	query := `SELECT COUNT(*), COALESCE(SUM(fact_count), 0) FROM stance_documents`

	err = s.pool.QueryRow(ctx, query).Scan(&totalStances, &totalFactsLinked)
	if err != nil {
		return 0, 0, fmt.Errorf("querying stance document counts: %w", err)
	}

	return totalStances, totalFactsLinked, nil
}

// DeleteStanceDocumentsForPerson deletes all stance documents for a person.
// The CASCADE on stance_document_facts handles the join table cleanup.
func (s *PostgresStorer) DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM stance_documents WHERE person_id = $1`, personID)
	if err != nil {
		return fmt.Errorf("deleting stance documents for person %d: %w", personID, err)
	}

	return nil
}

// GetStanceDocumentByID retrieves a single stance document by ID.
// Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) GetStanceDocumentByID(ctx context.Context, id int64) (*StanceDocument, error) {
	query := `
		SELECT id, person_id, cluster_label, cluster_embedding, summary, key_fact_ids, fact_count, last_updated_at, created_at
		FROM stance_documents
		WHERE id = $1`

	var doc StanceDocument
	var vec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&doc.ID, &doc.PersonID, &doc.ClusterLabel, &vec,
		&doc.Summary, &doc.KeyFactIDs, &doc.FactCount,
		&doc.LastUpdatedAt, &doc.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to get stance document %d: %w", id, err)
	}
	doc.ClusterEmbedding = vec.Slice()
	return &doc, nil
}

// DeleteStanceDocument deletes a single stance document by ID.
// The CASCADE on stance_document_facts handles join table cleanup.
// Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) DeleteStanceDocument(ctx context.Context, id int64) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM stance_documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete stance document %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	log.Info().Int64("stance_id", id).Msg("Deleted stance document")
	return nil
}

// UpdateStanceSummary rewrites a stance document's summary and cluster_label
// (used by the LLM-merge phase, which produces a new unified summary/label for
// the merged group). Centroid, key facts, and fact_count are left untouched —
// those are managed by the merge tx itself.
func (s *PostgresStorer) UpdateStanceSummary(ctx context.Context, stanceID int64, summary, clusterLabel string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("UpdateStanceSummary: summary must not be empty")
	}
	if strings.TrimSpace(clusterLabel) == "" {
		return fmt.Errorf("UpdateStanceSummary: cluster_label must not be empty")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE stance_documents
		SET summary = $1, cluster_label = $2, last_updated_at = NOW()
		WHERE id = $3`, summary, clusterLabel, stanceID)
	if err != nil {
		return fmt.Errorf("updating stance summary/label for %d: %w", stanceID, err)
	}
	return nil
}

// MergeStancesForPerson merges src into dst in a single transaction. Fact memberships
// are moved to dst (duplicates dropped via ON CONFLICT), contradictions whose bundle
// pointed at src are repointed to dst BEFORE the row is dropped (the FK is ON DELETE
// SET NULL, so a naive delete would silently null them), then dst's centroid and
// fact_count are updated and src is deleted. The new centroid is pre-computed by the
// caller (weighted normalised mean over members); summary and key_fact_ids on dst are
// preserved intentionally — dst is the larger stance.
func (s *PostgresStorer) MergeStancesForPerson(ctx context.Context, srcID, dstID int64, newDstCentroid []float32) error {
	if srcID == dstID {
		return fmt.Errorf("MergeStancesForPerson: src and dst are the same (%d)", srcID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin merge tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO stance_document_facts (stance_document_id, fact_id, added_at)
		SELECT $1, fact_id, added_at FROM stance_document_facts
		WHERE stance_document_id = $2
		ON CONFLICT (stance_document_id, fact_id) DO NOTHING`, dstID, srcID); err != nil {
		return fmt.Errorf("moving fact memberships %d->%d: %w", srcID, dstID, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM stance_document_facts WHERE stance_document_id = $1`, srcID); err != nil {
		return fmt.Errorf("clearing src memberships %d: %w", srcID, err)
	}

	if _, err := tx.Exec(ctx, `UPDATE contradictions SET bundle_stance_id = $1 WHERE bundle_stance_id = $2`, dstID, srcID); err != nil {
		return fmt.Errorf("repointing contradictions %d->%d: %w", srcID, dstID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE stance_documents
		SET cluster_embedding = $1,
		    fact_count = (SELECT count(*) FROM stance_document_facts WHERE stance_document_id = $2),
		    last_updated_at = NOW()
		WHERE id = $2`, pgvector.NewVector(newDstCentroid), dstID); err != nil {
		return fmt.Errorf("updating dst stance %d: %w", dstID, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM stance_documents WHERE id = $1`, srcID); err != nil {
		return fmt.Errorf("deleting src stance %d: %w", srcID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit merge %d->%d: %w", srcID, dstID, err)
	}
	return nil
}

// GetFactStanceMap returns a map fact_id -> stance_document_id for the given facts.
// Facts without a stance row are simply absent from the map.
func (s *PostgresStorer) GetFactStanceMap(ctx context.Context, factIDs []int64) (map[int64]int64, error) {
	if len(factIDs) == 0 {
		return map[int64]int64{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT fact_id, stance_document_id FROM stance_document_facts WHERE fact_id = ANY($1)`,
		factIDs)
	if err != nil {
		return nil, fmt.Errorf("querying fact stances: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int64, len(factIDs))
	for rows.Next() {
		var fid, sid int64
		if err := rows.Scan(&fid, &sid); err != nil {
			return nil, fmt.Errorf("scanning fact stance row: %w", err)
		}
		out[fid] = sid
	}
	return out, rows.Err()
}

// StanceCandidate is a stance returned by FindTopStancesForPerson.
type StanceCandidate struct {
	ID           int64
	ClusterLabel string
	Summary      string
	Distance     float64
}

// FindTopStancesForPerson returns up to `limit` stances for the person, ranked by cosine
// distance from the given embedding (lowest distance first). Used by the Bundler to pick
// LLM candidates when fact-level stance lookups don't agree.
func (s *PostgresStorer) FindTopStancesForPerson(ctx context.Context, personID int64, embedding []float32, limit int) ([]StanceCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, cluster_label, summary, cluster_embedding <-> $2 AS distance
		FROM stance_documents
		WHERE person_id = $1
		ORDER BY cluster_embedding <-> $2
		LIMIT $3`, personID, pgvector.NewVector(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("querying top stances for person %d: %w", personID, err)
	}
	defer rows.Close()
	var out []StanceCandidate
	for rows.Next() {
		var c StanceCandidate
		if err := rows.Scan(&c.ID, &c.ClusterLabel, &c.Summary, &c.Distance); err != nil {
			return nil, fmt.Errorf("scanning stance candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// OrphanFact carries the fields ProcessNewFact needs to assign an orphan
// (no stance link) to a stance: id, person-scoped fact text, embedding,
// and metadata used by topic assignment.
type OrphanFact struct {
	ID            int64
	FactText      string
	EmbeddingText string
	CanonicalVerb string
	Embedding     []float32
}

// ListPersonsWithOrphanFacts returns every person who has at least one
// non-duplicate fact not linked to any stance_document. Drives the
// per-person worker loop in `dm facts assign-stances`.
//
// Symmetric with ListOrphanFactsForPerson: facts marked as
// `fact_relations.relation_type = 'duplicate'` are excluded — duplicate
// facts are pointers to a canonical fact and do not get their own stance
// link by design. Without this filter, persons whose only orphans are
// duplicates loop forever in the assign-stances worker (the per-person
// orphan list comes back empty, the person is "skipped", but they still
// show up next iteration).
func (s *PostgresStorer) ListPersonsWithOrphanFacts(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, `
		SELECT DISTINCT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN fact_mentions fm ON fm.person_id = p.id
		JOIN fact_sources fs ON fs.id = fm.fact_source_id
		JOIN facts f ON f.id = fs.fact_id
		WHERE NOT EXISTS (
		    SELECT 1 FROM stance_document_facts sdf WHERE sdf.fact_id = f.id
		)
		AND NOT EXISTS (
		    SELECT 1 FROM fact_relations fr
		    WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		ORDER BY p.name`)
}

// ListOrphanFactsForPerson returns every active fact mentioning the person
// that is not linked to any stance_document. Skips facts marked as duplicates
// via fact_relations (mirrors the filter in cmd/stances_backfill.go's
// loadFactsForBackfill so we never try to assign a stance to a known dup).
func (s *PostgresStorer) ListOrphanFactsForPerson(ctx context.Context, personID int64) ([]OrphanFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
		       COALESCE(f.canonical_verb, ''), f.embedding
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		AND NOT EXISTS (
		    SELECT 1 FROM stance_document_facts sdf WHERE sdf.fact_id = f.id
		)
		AND NOT EXISTS (
		    SELECT 1 FROM fact_relations fr
		    WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		GROUP BY f.id
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("listing orphan facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	var out []OrphanFact
	for rows.Next() {
		var f OrphanFact
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.FactText, &f.EmbeddingText, &f.CanonicalVerb, &vec); err != nil {
			return nil, fmt.Errorf("scanning orphan fact: %w", err)
		}
		f.Embedding = vec.Slice()
		if len(f.Embedding) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SoloContradictionForBundling is the minimal payload the Bundler needs to retroactively
// resolve a contradiction whose bundle_stance_id is NULL.
type SoloContradictionForBundling struct {
	ID        int64
	PersonID  int64
	FactAID   int64
	FactBID   int64
	FactAText string
	FactBText string
	Reason    string
}

// BundleBackfillFastPath sets bundle_stance_id on every contradiction whose two facts now
// share a stance via stance_document_facts, but where the column is currently NULL. Returns
// the number of rows updated. Mirrors the SQL in migration 20260503000000 — used by the
// `dm contradictions bundle-backfill` CLI to re-run after a `stances rebuild` pass since
// the migration ran before stances were retroactively populated.
func (s *PostgresStorer) BundleBackfillFastPath(ctx context.Context, personID *int64) (int64, error) {
	args := []any{}
	personFilter := ""
	if personID != nil {
		args = append(args, *personID)
		personFilter = "AND c.person_id = $1"
	}
	query := fmt.Sprintf(`
		WITH shared AS (
			SELECT c.id AS contradiction_id, sa.stance_document_id
			FROM contradictions c
			JOIN stance_document_facts sa ON sa.fact_id = c.fact_a_id
			JOIN stance_document_facts sb ON sb.fact_id = c.fact_b_id
			                              AND sb.stance_document_id = sa.stance_document_id
			WHERE c.bundle_stance_id IS NULL
			%s
		)
		UPDATE contradictions c
		SET bundle_stance_id = shared.stance_document_id
		FROM shared
		WHERE shared.contradiction_id = c.id
		  AND c.bundle_stance_id IS NULL`, personFilter)
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("bundle backfill fast path: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListSoloContradictionsForBundling returns contradictions with bundle_stance_id IS NULL
// joined to fact text + reason, ready for the Bundler. Optionally scoped to a person and
// capped at limit (limit <= 0 means no cap).
func (s *PostgresStorer) ListSoloContradictionsForBundling(ctx context.Context, personID *int64, limit int) ([]SoloContradictionForBundling, error) {
	args := []any{}
	whereExtra := ""
	if personID != nil {
		args = append(args, *personID)
		whereExtra = fmt.Sprintf("AND c.person_id = $%d", len(args))
	}
	limitClause := ""
	if limit > 0 {
		args = append(args, limit)
		limitClause = fmt.Sprintf("LIMIT $%d", len(args))
	}
	query := fmt.Sprintf(`
		SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
		       fa.fact_text, fb.fact_text, COALESCE(c.reason, '')
		FROM contradictions c
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		WHERE c.bundle_stance_id IS NULL
		%s
		ORDER BY c.person_id, c.id
		%s`, whereExtra, limitClause)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list solo contradictions: %w", err)
	}
	defer rows.Close()
	out := make([]SoloContradictionForBundling, 0)
	for rows.Next() {
		var r SoloContradictionForBundling
		if err := rows.Scan(&r.ID, &r.PersonID, &r.FactAID, &r.FactBID, &r.FactAText, &r.FactBText, &r.Reason); err != nil {
			return nil, fmt.Errorf("scan solo contradiction: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetContradictionBundleStance assigns a bundle stance to a contradiction and recomputes the
// person's honesty score (since the bundle count may have changed). No-op if the contradiction
// already had the same bundle_stance_id. Used by the Bundler after an LLM-verified resolution.
func (s *PostgresStorer) SetContradictionBundleStance(ctx context.Context, contradictionID int64, bundleStanceID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var personID int64
	var prev *int64
	err = tx.QueryRow(ctx,
		`UPDATE contradictions SET bundle_stance_id = $1 WHERE id = $2 AND (bundle_stance_id IS DISTINCT FROM $1)
		 RETURNING person_id, bundle_stance_id`, bundleStanceID, contradictionID).Scan(&personID, &prev)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Already had that bundle — nothing to do.
			return nil
		}
		return fmt.Errorf("updating contradiction %d bundle: %w", contradictionID, err)
	}

	if err := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, personID, contradictionID); err != nil {
		return fmt.Errorf("recompute after bundle update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit bundle update: %w", err)
	}
	return nil
}
