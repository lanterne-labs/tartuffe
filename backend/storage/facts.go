package storage

import (
	"context"
	"encoding/json"

	"digital-mirror/backend/core"
	"digital-mirror/backend/metrics"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

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
// Each hit carries its canonical verb and the number of distinct persons
// mentioning it, so the dedupe tiers of the inserter need no per-candidate lookup.
// Returns up to limit results (default 20).
func (s *PostgresStorer) FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, factText string, textFloor float64, limit int) ([]core.SimilarFact, error) {
	timer := metrics.NewQueryTimer("find_similar_active_facts")
	start := time.Now()

	if limit <= 0 {
		limit = 20
	}

	// Two candidate sources, unioned: embedding distance on the stance-blind
	// topic slug, and trigram similarity on the fact text. The slug is
	// nondeterministic across extractions of one statement (measured on prod:
	// the same quote embedded at cosine distance 0.47 from itself under two
	// slugs), so near-verbatim restatements often fall outside the embedding
	// window while their text still matches; the LLM gate decides either way.
	query := `
		WITH PersonFacts AS (
			SELECT DISTINCT fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = ANY($1)
		),
		Active AS (
			SELECT f.id, f.fact_text, f.embedding <=> $2 AS distance,
			       COALESCE(f.canonical_verb, '') AS canonical_verb
			FROM facts f
			JOIN PersonFacts pf ON f.id = pf.fact_id
			WHERE NOT EXISTS (
			      SELECT 1 FROM fact_relations fr
			      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
		),
		Hits AS (
			SELECT * FROM Active
			WHERE distance <= $3
			   OR ($5 <> '' AND similarity(fact_text, $5) >= $6)
			ORDER BY distance
			LIMIT $4
		)
		SELECT h.id, h.fact_text, h.distance, h.canonical_verb,
		       (SELECT COUNT(DISTINCT fm2.person_id)
		        FROM fact_mentions fm2
		        JOIN fact_sources fs2 ON fs2.id = fm2.fact_source_id
		        WHERE fs2.fact_id = h.id) AS mention_persons
		FROM Hits h
		ORDER BY h.distance
	`

	rows, err := s.pool.Query(ctx, query,
		personIDs,
		pgvector.NewVector(embedding),
		threshold,
		limit,
		factText,
		textFloor,
	)
	if err != nil {
		timer.ObserveDuration(err)
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var results []core.SimilarFact
	for rows.Next() {
		var sf core.SimilarFact
		if err := rows.Scan(&sf.ID, &sf.Text, &sf.Distance, &sf.CanonicalVerb, &sf.MentionPersons); err != nil {
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

	// The active fact set changed: refresh fact_count and scores for every affected person.
	if err := s.refreshPersonScores(ctx, tx, personIDs); err != nil {
		return err
	}

	// Suppress per-item debug log to keep CLI output concise
	return nil // Success
}

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

	// Clear the plugins' per-person processing state so they re-process the
	// person after the facts are gone (see PersonTable).
	if err := resetPersonTables(ctx, tx, personID); err != nil {
		return 0, err
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
	ownScore := core.ComputeHonestyScore(factsCount, stats)

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
	aggregate := core.ComputeAggregateScore(ownScore, membersScore)

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

// GetFactByID retrieves a single fact by its ID for deduplication.
func (s *PostgresStorer) GetFactByID(ctx context.Context, factID int64) (*core.FactForDedup, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
			   COALESCE(f.canonical_verb, ''), f.embedding,
			   NOT EXISTS (
			       SELECT 1 FROM fact_relations fr
			       WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			   ) AS is_active
		FROM facts f
		WHERE f.id = $1`

	var f core.FactForDedup
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
