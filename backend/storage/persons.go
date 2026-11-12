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
// accent- and case-insensitively through personIDByFoldedName (the same
// lookup the insert paths use, so a name that inserts as an existing row also
// resolves to it here). A miss wraps pgx.ErrNoRows.
func (s *PostgresStorer) GetPersonIDByName(ctx context.Context, name string) (int64, error) {
	id, err := personIDByFoldedName(ctx, s.pool, util.NormalizePersonName(name))
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, fmt.Errorf("failed to find person by name %s: %w", name, pgx.ErrNoRows)
	}
	return id, nil
}

// GetPersonIDBySlug returns the id of the person whose page is
// /person/<slug>, or 0 when no person has the slug.
func (s *PostgresStorer) GetPersonIDBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM persons WHERE slug = $1`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("person by slug %q: %w", slug, err)
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

// FindOrInsertPersons resolves personNames (normalized, deduplicated, in
// input order) to person IDs in one transaction, inserting the unknown ones as
// entity_type human. A candidate the person gate rejects is dropped from the
// result (the caller sees a shorter slice); only when every name was rejected
// and none pre-existed does the first rejection come back as the error, so
// core.FactInserter can decide whether the fact still has a subject. Empty
// names after normalization are skipped with a warning.
func (s *PostgresStorer) FindOrInsertPersons(ctx context.Context, personNames []string) ([]int64, error) {
	if len(personNames) == 0 {
		return []int64{}, nil
	}
	seen := make(map[string]struct{}, len(personNames))
	names := make([]string, 0, len(personNames))
	for _, original := range personNames {
		normalized := util.NormalizePersonName(original)
		if normalized == "" {
			log.Warn().Str("original_name", original).Msg("Skipping empty person name during find/insert")
			continue
		}
		if _, dup := seen[normalized]; !dup {
			seen[normalized] = struct{}{}
			names = append(names, normalized)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no valid persons after normalization: %v", personNames)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) // Rollback if not committed

	// Names resolved one at a time inside the tx: a later name that folds to a
	// row inserted earlier in this loop resolves to it instead of duplicating.
	personIDs := make([]int64, 0, len(names))
	var firstRejection *core.EntityRejectedError
	for _, name := range names {
		id, _, err := s.resolveOrInsertPerson(ctx, tx, name, "")
		if err != nil {
			var rej *core.EntityRejectedError
			if errors.As(err, &rej) {
				if firstRejection == nil {
					firstRejection = rej
				}
				continue
			}
			return nil, err
		}
		personIDs = append(personIDs, id)
	}
	if len(personIDs) == 0 && firstRejection != nil {
		return nil, firstRejection
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

	// A gate rejection surfaces as *core.EntityRejectedError so callers can
	// soft-fail this fact and keep going on the next one.
	id, _, err := s.resolveOrInsertPerson(ctx, s.pool, normalized, entityType)
	return id, err
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
	return s.resolveOrInsertPerson(ctx, s.pool, normalizedName, "")
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
	return s.resolveOrInsertPerson(ctx, s.pool, normalizedName, entityType)
}

// PersonSummary represents a summary of a person for the API
type PersonSummary struct {
	ID int64 `json:"id"`
	// Slug names the person's page (/person/<slug>): set from the name at
	// the insert, never changed (the migration 20261111100000).
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	EntityType   *string  `json:"entity_type,omitempty"`
	HonestyScore float64  `json:"honesty_score"`
	OwnScore     *float64 `json:"own_score,omitempty"`
	MembersScore *float64 `json:"members_score,omitempty"`
	ImageURL     *string  `json:"image_url,omitempty"`
	// Candidate: the AI work after extraction (stances, contradiction
	// detection) runs for this person (candidates.go).
	Candidate          bool `json:"candidate"`
	FactCount          int  `json:"fact_count"`
	StanceCount        int  `json:"stance_count"`
	ContradictionCount int  `json:"contradiction_count"`
	// CollectedFrom and CollectedTo bound the period the person's
	// statements were collected over (GetPersonByID only): the periods
	// of the collection tasks that ended, else the dates of the facts.
	CollectedFrom *time.Time `json:"collected_from,omitempty"`
	CollectedTo   *time.Time `json:"collected_to,omitempty"`
	// LastContradictionAt is the date of the person's latest statement or
	// act that contradicts another of theirs (GetPersonByID only): the
	// later fact of their most recent contradiction, at the date the site
	// shows for it. LastContradictionPrecision is the precision of a
	// statement date ("year", "month", "day"), absent for a source's date.
	LastContradictionAt        *time.Time `json:"last_contradiction_at,omitempty"`
	LastContradictionPrecision *string    `json:"last_contradiction_precision,omitempty"`
	ScoreConfidence
}

// GetPersonsRankedByScore returns persons ranked by the chosen sort order.
// entityType can be empty (all), "party", or "human" to filter results.
// search performs case-insensitive partial match on name.
// sort can be "honesty_desc" (highest score first), "honesty_asc" (lowest first),
// or empty for the default (fact count desc, then honesty desc).
// minFacts filters out persons with fewer than minFacts (deduped) facts.
// candidate keeps the candidates ("true"), the others ("false") or everyone ("").
func (s *PostgresStorer) GetPersonsRankedByScore(ctx context.Context, limit int, offset int, entityType string, search string, sort string, minFacts int, candidate string) ([]PersonSummary, error) {
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
		SELECT `+PersonSummaryColumns+`, p.fact_count,
			COALESCE((SELECT COUNT(*) FROM stance_documents WHERE person_id = p.id), 0) AS stance_count,
			COALESCE((SELECT COUNT(*) FROM contradictions WHERE person_id = p.id), 0) AS contradiction_count
		FROM persons p
		WHERE ($3 = '' OR p.entity_type = $3)
		  AND ($4 = '' OR p.name ILIKE '%%' || $4 || '%%')
		  AND p.fact_count >= $5
		  AND ($6 = '' OR ($6 = 'true') = (`+CandidateSQL+`))
		ORDER BY %s
		LIMIT $1 OFFSET $2`, orderBy)

	rows, err := s.pool.Query(ctx, query, limit, offset, entityType, search, minFacts, candidate)
	if err != nil {
		return nil, fmt.Errorf("failed to query persons: %w", err)
	}
	defer rows.Close()

	var persons []PersonSummary
	for rows.Next() {
		var person PersonSummary
		if err := ScanPersonSummary(rows, &person, &person.FactCount, &person.StanceCount, &person.ContradictionCount); err != nil {
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
		SELECT ` + PersonSummaryColumns + `,
			COUNT(DISTINCT fs.fact_id) FILTER (
				WHERE NOT EXISTS (SELECT 1 FROM fact_relations fr WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate')
			) AS fact_count,
			COALESCE((SELECT COUNT(*) FROM stance_documents WHERE person_id = p.id), 0) AS stance_count,
			COALESCE((SELECT COUNT(*) FROM contradictions  WHERE person_id = p.id), 0) AS contradiction_count
		FROM persons p
		LEFT JOIN fact_mentions fm ON p.id = fm.person_id
		LEFT JOIN fact_sources fs ON fm.fact_source_id = fs.id
		WHERE p.id = $1
		GROUP BY ` + PersonSummaryColumns

	var person PersonSummary
	err := ScanPersonSummary(s.pool.QueryRow(ctx, query, personID), &person,
		&person.FactCount, &person.StanceCount, &person.ContradictionCount)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query person %d: %w", personID, err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(t.since, f.since::date), COALESCE(t.until, f.until::date)
		FROM (SELECT MIN(since) AS since, MAX(until) - 1 AS until FROM collection_tasks WHERE person_id = $1 AND status = 'done') t,
		     (SELECT MIN(d) AS since, MAX(d) AS until
		      FROM (SELECT COALESCE(fa.statement_date, fs.published_at) AS d
		            FROM fact_mentions fm
		            JOIN fact_sources fs ON fs.id = fm.fact_source_id
		            JOIN facts fa ON fa.id = fs.fact_id
		            WHERE fm.person_id = $1) x) f`, personID).Scan(&person.CollectedFrom, &person.CollectedTo); err != nil {
		return nil, fmt.Errorf("failed to query the collected period of person %d: %w", personID, err)
	}
	// The latest dated fact of the person's contradictions: the later fact
	// of a pair is the one that made the contradiction.
	err = s.pool.QueryRow(ctx, `
		SELECT d.at, d.precision
		FROM (
			SELECT `+shownDateSQL("f", "(SELECT MIN(published_at) FROM fact_sources WHERE fact_id = f.id)")+` AS at,
			       CASE WHEN f.statement_date IS NOT NULL THEN f.statement_date_precision END AS precision
			FROM facts f
			WHERE f.id IN (
				SELECT fact_a_id FROM contradictions WHERE person_id = $1
				UNION SELECT fact_b_id FROM contradictions WHERE person_id = $1
			)
		) d
		WHERE d.at IS NOT NULL AND d.at <= now()
		ORDER BY d.at DESC
		LIMIT 1`, personID).Scan(&person.LastContradictionAt, &person.LastContradictionPrecision)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("failed to query the last contradiction of person %d: %w", personID, err)
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
		SELECT DISTINCT ` + PersonSummaryColumns + `
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
		if err := ScanPersonSummary(rows, &person); err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}
		persons = append(persons, person)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating persons: %w", rows.Err())
	}

	return persons, nil
}
