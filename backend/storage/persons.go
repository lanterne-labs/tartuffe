package storage

import (
	"context"
	"errors"

	"digital-mirror/backend/core"
	"digital-mirror/backend/util"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Affiliation represents a temporal relationship between a person and a party
type Affiliation struct {
	PartyID    int64
	StartTime  *time.Time
	EndTime    *time.Time
	Attributes map[string]any
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

// GetPersonIDByAttribute returns the person.id whose persons.attributes[key]
// equals value. key is a fixed identifier chosen by the caller, never user
// input.
func (s *PostgresStorer) GetPersonIDByAttribute(ctx context.Context, key, value string) (int64, error) {
	return s.personIDByColumn(ctx, key, fmt.Sprintf("attributes->>'%s'", key), value)
}

// GetPersonIDByName returns the person.id for the given name, matched
// accent-insensitively: the input is normalized to the canonical accent-free
// person form and compared against immutable_unaccent(name), so it resolves
// whether stored names are already accent-free or still carry accents (during
// the one-time unaccent migration).
func (s *PostgresStorer) GetPersonIDByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM persons WHERE lower(immutable_unaccent(name)) = $1 LIMIT 1`,
		util.NormalizePersonName(name)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to find person by name %s: %w", name, err)
	}
	return id, nil
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
	normalized := util.NormalizePersonName(name)
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

	// Person is NEW — run it through the person gate before inserting. A rejection
	// surfaces as *core.EntityRejectedError so callers can soft-fail this fact and keep
	// going on the next one.
	if err := s.checkPersonGate(ctx, normalized, entityType); err != nil {
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
		normalized := util.NormalizePersonName(originalName)
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
	//    map) go through the person gate; rejected ones are logged and dropped so
	//    the rest of the batch keeps moving.
	namesToInsert := make([]string, 0)
	finalPersonIDsMap := make(map[string]int64) // Store all IDs (existing + new) by normalized name
	rejectedNames := make([]string, 0)
	for _, normalizedName := range normalizedNamesList {
		if id, exists := existingPersons[normalizedName]; exists {
			finalPersonIDsMap[normalizedName] = id
			continue
		}
		// New candidate — gate it. Rejection drops it from this batch.
		if err := s.checkPersonGate(ctx, normalizedName, ""); err != nil {
			var rej *core.EntityRejectedError
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
		return nil, &core.EntityRejectedError{
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
	// Gate-rejected names are silently omitted (already logged by the gate in
	// step 3). The remaining missing-from-map case is a real bug.
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

// PersonInfo represents a person with their details.
type PersonInfo struct {
	ID         int64
	Name       string
	EntityType string
}

// allPersonsSQL lists every person as (id, name, entity_type), by name.
const allPersonsSQL = `SELECT id, name, COALESCE(entity_type, 'person') as entity_type FROM persons ORDER BY name`

// GetAllPersonRecords returns all persons as core.PersonRecord, satisfying core.PersonResolver.
func (s *PostgresStorer) GetAllPersonRecords(ctx context.Context) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, allPersonsSQL)
}

// queryPersonRecords runs a query selecting (id, name, entity_type) and scans
// the rows into core.PersonRecord, the shape the core sweeps take.
func (s *PostgresStorer) queryPersonRecords(ctx context.Context, query string, args ...any) ([]core.PersonRecord, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []core.PersonRecord
	for rows.Next() {
		var p core.PersonRecord
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

// queryPersonInfos is queryPersonRecords in the PersonInfo shape. Backs the
// GetPersons* / list helpers below.
func (s *PostgresStorer) queryPersonInfos(ctx context.Context, query string, args ...any) ([]PersonInfo, error) {
	records, err := s.queryPersonRecords(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	persons := make([]PersonInfo, len(records))
	for i, r := range records {
		persons[i] = PersonInfo(r)
	}
	return persons, nil
}

// GetAllPersons retrieves all persons with their details (id, name, entity_type).
func (s *PostgresStorer) GetAllPersons(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, allPersonsSQL)
}

// GetPersonsWithMultipleStances returns only the persons who have at least 2
// stance documents. Used by the daily merge sweep to avoid the cost of
// inspecting the thousands of single-stance / no-stance persons that can't
// possibly produce a merge candidate.
func (s *PostgresStorer) GetPersonsWithMultipleStances(ctx context.Context) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, `
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
// in the table) are run through the person gate first; rejected candidates
// surface as *core.EntityRejectedError so callers can decide whether to skip them.
func (s *PostgresStorer) AddPerson(ctx context.Context, name string) (id int64, inserted bool, err error) {
	normalizedName := util.NormalizePersonName(name)
	if normalizedName == "" {
		return 0, false, fmt.Errorf("cannot add empty person name")
	}

	// Fast path: if the person already exists, skip the gate (they were gated
	// when first inserted OR pre-date the gate). Gate only NEW candidates.
	if existing, lookupErr := s.findPersonIDByName(ctx, normalizedName); lookupErr == nil && existing > 0 {
		return existing, false, nil
	}
	if err := s.checkPersonGate(ctx, normalizedName, ""); err != nil {
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
	normalizedName := util.NormalizePersonName(name)
	if normalizedName == "" {
		return 0, false, fmt.Errorf("cannot add empty person name")
	}

	// Validate entity_type
	validTypes := map[string]bool{"human": true, "party": true, "company": true, "organization": true}
	if !validTypes[entityType] {
		return 0, false, fmt.Errorf("invalid entity_type '%s': must be one of 'human', 'party', 'company', 'organization'", entityType)
	}

	// Fast path: existing persons skip the gate; only NEW candidates get checked.
	if existing, lookupErr := s.findPersonIDByName(ctx, normalizedName); lookupErr == nil && existing > 0 {
		return existing, false, nil
	}
	if err := s.checkPersonGate(ctx, normalizedName, entityType); err != nil {
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
