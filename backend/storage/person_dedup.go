package storage

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// DuplicateCandidate is a single trigram-similar pair of persons that may be
// the same real-world entity.
type DuplicateCandidate struct {
	IDA         int64
	IDB         int64
	NameA       string
	NameB       string
	EntityTypeA string
	EntityTypeB string
	FactCountA  int
	FactCountB  int
	Similarity  float64
}

// DedupMember is a person row enriched with the signals the dedup CLI needs to
// pick a canonical survivor.
type DedupMember struct {
	ID         int64
	Name       string
	EntityType string
	FactCount  int
	// Curated is true when the row is a curated French Assemblée entity (has an
	// organeRef or assemblee_id attribute) — such rows are preferred survivors.
	Curated bool
}

// ChooseCanonical picks the survivor of a confirmed same-entity cluster using
// curated-aware rules (defends against deleting real curated entities):
//  1. exactly ONE curated member -> that member is canonical (merge the junk
//     dup INTO the curated entity, never the reverse).
//  2. TWO OR MORE curated members -> skip the whole cluster (never merge two
//     real curated entities); returns ok=false.
//  3. no curated member -> highest fact_count, tie-break lowest id.
func ChooseCanonical(members []DedupMember) (DedupMember, bool) {
	var curated []DedupMember
	for _, m := range members {
		if m.Curated {
			curated = append(curated, m)
		}
	}
	if len(curated) == 1 {
		return curated[0], true
	}
	if len(curated) >= 2 {
		ids := make([]int64, len(curated))
		names := make([]string, len(curated))
		for i, m := range curated {
			ids[i] = m.ID
			names[i] = m.Name
		}
		log.Warn().
			Ints64("curated_ids", ids).
			Strs("curated_names", names).
			Msg("Cluster has 2+ curated members; refusing to merge real entities, skipping")
		return DedupMember{}, false
	}

	best := members[0]
	for _, m := range members[1:] {
		if m.FactCount > best.FactCount || (m.FactCount == best.FactCount && m.ID < best.ID) {
			best = m
		}
	}
	return best, true
}

// GetPersonsForDedup fetches the dedup-relevant fields for the given person IDs
// in a single round-trip.
func (s *PostgresStorer) GetPersonsForDedup(ctx context.Context, ids []int64) ([]DedupMember, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const query = `
		SELECT id, name, entity_type, fact_count,
		       (attributes ? 'organeRef' OR attributes ? 'assemblee_id') AS curated
		FROM persons
		WHERE id = ANY($1)`

	rows, err := s.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to query dedup members: %w", err)
	}
	defer rows.Close()

	var members []DedupMember
	for rows.Next() {
		var m DedupMember
		if err := rows.Scan(&m.ID, &m.Name, &m.EntityType, &m.FactCount, &m.Curated); err != nil {
			return nil, fmt.Errorf("failed to scan dedup member: %w", err)
		}
		members = append(members, m)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating dedup members: %w", rows.Err())
	}
	return members, nil
}

// MergePersons folds person `fromID` into `intoID` inside a single transaction:
// every row referencing `fromID` is repointed to `intoID` (or dropped when a
// repoint would violate a UNIQUE constraint), the `fromID` person row is
// deleted, and the survivor's stance fact counts + honesty score are
// recomputed. Order matters — stance memberships are moved before stance rows
// so the "every fact has a stance" invariant is never broken.
func (s *PostgresStorer) MergePersons(ctx context.Context, fromID, intoID int64) error {
	if fromID == intoID {
		return fmt.Errorf("cannot merge a person into itself (id=%d)", fromID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin merge transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// a. Guard: both rows must exist. A missing survivor is an error; a missing
	//    source is a no-op (already merged / never existed).
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM persons WHERE id = $1)`, intoID).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check survivor person %d: %w", intoID, err)
	}
	if !exists {
		return fmt.Errorf("merge target person %d does not exist", intoID)
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM persons WHERE id = $1)`, fromID).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check source person %d: %w", fromID, err)
	}
	if !exists {
		// Nothing to merge.
		return nil
	}

	// b. Move stance memberships for stances that will collide (same
	//    cluster_label on the survivor) BEFORE we touch any stance row, so the
	//    facts keep a stance throughout.
	if _, err := tx.Exec(ctx, `
		WITH collided AS (
		  SELECT s_from.id AS from_sid, s_into.id AS into_sid
		  FROM stance_documents s_from
		  JOIN stance_documents s_into ON s_into.person_id=$2 AND s_into.cluster_label=s_from.cluster_label
		  WHERE s_from.person_id=$1
		)
		UPDATE stance_document_facts sdf SET stance_document_id=c.into_sid
		FROM collided c
		WHERE sdf.stance_document_id=c.from_sid
		  AND NOT EXISTS (SELECT 1 FROM stance_document_facts x WHERE x.stance_document_id=c.into_sid AND x.fact_id=sdf.fact_id)`,
		fromID, intoID); err != nil {
		return fmt.Errorf("failed to move stance memberships: %w", err)
	}

	// c. Conflict-safe repoints: only move rows whose new (person_id, ...) would
	//    not violate the table's UNIQUE constraint. Colliding rows are cleaned up
	//    in step d.
	repoints := []struct {
		name string
		sql  string
	}{
		{"fact_mentions", `UPDATE fact_mentions fm SET person_id=$2 WHERE fm.person_id=$1 AND NOT EXISTS (SELECT 1 FROM fact_mentions x WHERE x.fact_source_id=fm.fact_source_id AND x.person_id=$2)`},
		{"contradictions", `UPDATE contradictions c SET person_id=$2 WHERE c.person_id=$1 AND NOT EXISTS (SELECT 1 FROM contradictions x WHERE x.person_id=$2 AND x.fact_a_id=c.fact_a_id AND x.fact_b_id=c.fact_b_id)`},
		{"fact_pair_checks", `UPDATE fact_pair_checks fpc SET person_id=$2 WHERE fpc.person_id=$1 AND NOT EXISTS (SELECT 1 FROM fact_pair_checks x WHERE x.person_id=$2 AND x.fact_a_id=fpc.fact_a_id AND x.fact_b_id=fpc.fact_b_id)`},
		{"stance_documents", `UPDATE stance_documents s SET person_id=$2 WHERE s.person_id=$1 AND NOT EXISTS (SELECT 1 FROM stance_documents x WHERE x.person_id=$2 AND x.cluster_label=s.cluster_label)`},
		{"twitter_handles", `UPDATE twitter_handles th SET person_id=$2 WHERE th.person_id=$1 AND NOT EXISTS (SELECT 1 FROM twitter_handles x WHERE x.person_id=$2 AND x.handle=th.handle)`},
		{"search_queue", `UPDATE search_queue q SET person_id=$2 WHERE q.person_id=$1 AND NOT EXISTS (SELECT 1 FROM search_queue x WHERE x.person_id=$2)`},
		{"person_links_src", `UPDATE person_links pl SET src_person_id=$2 WHERE pl.src_person_id=$1 AND pl.dst_person_id<>$2 AND NOT EXISTS (SELECT 1 FROM person_links x WHERE x.src_person_id=$2 AND x.dst_person_id=pl.dst_person_id AND x.link_type=pl.link_type)`},
		{"person_links_dst", `UPDATE person_links pl SET dst_person_id=$2 WHERE pl.dst_person_id=$1 AND pl.src_person_id<>$2 AND NOT EXISTS (SELECT 1 FROM person_links x WHERE x.dst_person_id=$2 AND x.src_person_id=pl.src_person_id AND x.link_type=pl.link_type)`},
		{"news_search_log", `UPDATE news_search_log SET person_id=$2 WHERE person_id=$1`},
		{"rss_feeds", `UPDATE rss_feeds SET person_id=$2 WHERE person_id=$1`},
	}
	for _, r := range repoints {
		if _, err := tx.Exec(ctx, r.sql, fromID, intoID); err != nil {
			return fmt.Errorf("failed to repoint %s: %w", r.name, err)
		}
	}

	// d. Clean up the leftover from-rows (the ones that collided and were not
	//    repointed) so the persons delete won't FK-fail. Deleting leftover
	//    stance_documents cascades their stance_document_facts — fine, those
	//    facts were already moved into the surviving stance in step b.
	cleanups := []struct {
		name string
		sql  string
	}{
		{"fact_mentions", `DELETE FROM fact_mentions WHERE person_id=$1`},
		{"contradictions", `DELETE FROM contradictions WHERE person_id=$1`},
		{"fact_pair_checks", `DELETE FROM fact_pair_checks WHERE person_id=$1`},
		{"stance_documents", `DELETE FROM stance_documents WHERE person_id=$1`},
		{"twitter_handles", `DELETE FROM twitter_handles WHERE person_id=$1`},
		{"search_queue", `DELETE FROM search_queue WHERE person_id=$1`},
		{"person_links", `DELETE FROM person_links WHERE src_person_id=$1 OR dst_person_id=$1`},
	}
	for _, c := range cleanups {
		if _, err := tx.Exec(ctx, c.sql, fromID); err != nil {
			return fmt.Errorf("failed to clean up leftover %s: %w", c.name, err)
		}
	}

	// e. Delete the source person row.
	if _, err := tx.Exec(ctx, `DELETE FROM persons WHERE id=$1`, fromID); err != nil {
		return fmt.Errorf("failed to delete merged person %d: %w", fromID, err)
	}

	// f. Refresh the survivor's stance fact_count to reflect moved memberships.
	if _, err := tx.Exec(ctx, `
		UPDATE stance_documents s
		SET fact_count=(SELECT count(*) FROM stance_document_facts WHERE stance_document_id=s.id)
		WHERE s.person_id=$1`, intoID); err != nil {
		return fmt.Errorf("failed to refresh survivor stance fact_count: %w", err)
	}

	// g. Recompute the survivor's fact_count + honesty + parent cascade.
	if err := s.recomputeScoreAfterFactDeletion(ctx, tx, intoID, 0); err != nil {
		return fmt.Errorf("failed to recompute score after merge: %w", err)
	}

	// h. Commit.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit merge: %w", err)
	}

	log.Info().Int64("from_id", fromID).Int64("into_id", intoID).Msg("Merged person")
	return nil
}

// FindDuplicateCandidates returns trigram-similar person pairs above `threshold`
// (each unordered pair once, a.id < b.id). When entityType is non-empty only
// pairs of that type are considered. Pairs where BOTH rows are curated French
// Assemblée entities (both have organeRef, or both have assemblee_id) are
// excluded — those are real distinct entities, not dupes.
func (s *PostgresStorer) FindDuplicateCandidates(ctx context.Context, entityType string, threshold float64) ([]DuplicateCandidate, error) {
	// Use the pg_trgm `%` operator (gated by set_limit) so the join can use the
	// trigram GIN index `idx_persons_name_trgm` instead of recomputing
	// similarity() for every pair (O(n²) ≈ 9M comparisons on ~4k rows). The
	// `%` threshold is session-scoped, so set_limit must run on the same
	// connection as the query — acquire one conn and reuse it.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire connection for duplicate candidates: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT set_limit($1)`, threshold); err != nil {
		return nil, fmt.Errorf("failed to set trigram similarity threshold: %w", err)
	}

	const query = `
		SELECT a.id, a.name, a.entity_type, a.fact_count,
		       b.id, b.name, b.entity_type, b.fact_count,
		       similarity(a.name, b.name) AS sim
		FROM persons a JOIN persons b ON a.id < b.id
		WHERE ($1 = '' OR a.entity_type = $1) AND ($1 = '' OR b.entity_type = $1)
		  AND a.name % b.name
		  AND NOT (a.attributes ? 'organeRef' AND b.attributes ? 'organeRef')
		  AND NOT (a.attributes ? 'assemblee_id' AND b.attributes ? 'assemblee_id')
		ORDER BY sim DESC`

	rows, err := conn.Query(ctx, query, entityType)
	if err != nil {
		return nil, fmt.Errorf("failed to query duplicate candidates: %w", err)
	}
	defer rows.Close()

	var candidates []DuplicateCandidate
	for rows.Next() {
		var c DuplicateCandidate
		if err := rows.Scan(&c.IDA, &c.NameA, &c.EntityTypeA, &c.FactCountA,
			&c.IDB, &c.NameB, &c.EntityTypeB, &c.FactCountB, &c.Similarity); err != nil {
			return nil, fmt.Errorf("failed to scan duplicate candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating duplicate candidates: %w", rows.Err())
	}
	return candidates, nil
}
