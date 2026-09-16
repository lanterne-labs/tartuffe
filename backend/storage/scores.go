package storage

import (
	"context"
	"encoding/json"

	"digital-mirror/backend/core"
	"digital-mirror/backend/metrics"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

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

	ownScore := core.ComputeHonestyScore(factsCount, stats)

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

	aggregate := core.ComputeAggregateScore(ownScore, membersScore)

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

// rowQueryer is the read surface shared by a pool and a transaction, for
// helpers that run one SELECT inside or outside a transaction.
type rowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// computeMembersScore calculates the average honesty_score of active members for an entity.
// Returns nil if no active members exist.
func (s *PostgresStorer) computeMembersScore(ctx context.Context, q rowQueryer, entityID int64) (*float64, error) {
	query := `
		SELECT AVG(p.honesty_score) FROM persons p
		JOIN person_links pl ON p.id = pl.src_person_id
		WHERE pl.dst_person_id = $1
		  AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)`

	var avg *float64
	err := q.QueryRow(ctx, query, entityID).Scan(&avg)
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

		aggregate := core.ComputeAggregateScore(ownScore, membersScore)

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

// getPersonFactsCount returns the number of distinct ACTIVE facts mentioning a
// person: facts folded under a meta (or plain duplicate-marked facts) count via
// their canonical fact only, so repeating the same claim N times weighs like
// once in the honesty-score denominator. Matches refreshPersonFactCount.
func (s *PostgresStorer) getPersonFactsCount(ctx context.Context, tx pgx.Tx, personID int64) (int, error) {
	query := `
		SELECT COUNT(DISTINCT f.id)
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM fact_relations fr
			WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )`

	var count int
	err := tx.QueryRow(ctx, query, personID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count facts for person %d: %w", personID, err)
	}
	return count, nil
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
// using core.ComputeHonestyScore. If dryRun is true, scores are not written.
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
			WHERE NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			)
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
		u.OwnScore = core.ComputeHonestyScore(u.Facts, core.ContradictionStats{Bundles: u.Bundles, Total: u.Total})
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
				updates[i].NewScore = core.ComputeAggregateScore(u.OwnScore, membersScore)

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
				avg, err := s.computeMembersScore(ctx, s.pool, u.PersonID)
				if err != nil {
					return nil, fmt.Errorf("failed to compute members score for entity %d: %w", u.PersonID, err)
				}
				updates[i].MembersScore = avg
				updates[i].NewScore = core.ComputeAggregateScore(u.OwnScore, avg)
			}
		}
	}

	return updates, nil
}
