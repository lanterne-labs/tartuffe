package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RefreshPersonScore recomputes one person's fact_count and scores from live rows,
// outside a caller-owned transaction. Used after fold operations mark new duplicates.
func (s *PostgresStorer) RefreshPersonScore(ctx context.Context, personID int64) error {
	return s.RefreshPersonScores(ctx, []int64{personID})
}

// RefreshPersonScores is the batch form of RefreshPersonScore. Used after bulk mention
// fan-out (shared vote facts attribute to every voter).
func (s *PostgresStorer) RefreshPersonScores(ctx context.Context, personIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin score refresh tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.refreshPersonScores(ctx, tx, personIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// refreshPersonScores recomputes fact_count, own_score, members_score and honesty_score
// for the given persons from live rows, then the aggregate of any parent entity they
// belong to. Four round trips for the whole batch, no events: this is the silent
// "fact set changed" refresh, as opposed to recomputeHonestyScoreAndEmitEvents on a
// contradiction. Without it own_score only moves on contradiction events and drifts
// from the live formula as facts accumulate.
func (s *PostgresStorer) refreshPersonScores(ctx context.Context, tx pgx.Tx, personIDs []int64) error {
	if len(personIDs) == 0 {
		return nil
	}

	inputs := `
		SELECT p.id,
		       COALESCE(fc.cnt, 0)     AS facts_count,
		       COALESCE(cc.bundles, 0) AS bundles,
		       COALESCE(cc.total, 0)   AS total
		FROM persons p
		LEFT JOIN (
			SELECT fm.person_id, COUNT(DISTINCT fs.fact_id) FILTER (
				WHERE NOT EXISTS (
					SELECT 1 FROM fact_relations fr
					WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate'
				)
			) AS cnt
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = ANY($1)
			GROUP BY fm.person_id
		) fc ON fc.person_id = p.id
		LEFT JOIN (
			SELECT person_id,
			       COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:'||id::text)) AS bundles,
			       COUNT(*) AS total
			FROM contradictions
			WHERE person_id = ANY($1)
			GROUP BY person_id
		) cc ON cc.person_id = p.id
		WHERE p.id = ANY($1)`

	rows, err := tx.Query(ctx, inputs, personIDs)
	if err != nil {
		return fmt.Errorf("query score inputs for %d persons: %w", len(personIDs), err)
	}
	var ids []int64
	var owns []float64
	var counts []int
	for rows.Next() {
		var id int64
		var facts int
		var stats ContradictionStats
		if err := rows.Scan(&id, &facts, &stats.Bundles, &stats.Total); err != nil {
			rows.Close()
			return fmt.Errorf("scan score inputs: %w", err)
		}
		ids = append(ids, id)
		owns = append(owns, ComputeHonestyScore(facts, stats))
		counts = append(counts, facts)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read score inputs: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	// Humans carry their own score as the headline; non-humans are corrected below.
	_, err = tx.Exec(ctx, `
		UPDATE persons p SET own_score = u.own, honesty_score = u.own, fact_count = u.cnt
		FROM unnest($1::bigint[], $2::float8[], $3::int[]) AS u(id, own, cnt)
		WHERE p.id = u.id`, ids, owns, counts)
	if err != nil {
		return fmt.Errorf("update own scores: %w", err)
	}

	// Entities whose aggregate depends on this batch: the non-human members of the batch
	// themselves, plus every parent the batch links into.
	aggregates := `
		SELECT e.id, e.own_score, AVG(m.honesty_score)
		FROM persons e
		LEFT JOIN person_links pl ON pl.dst_person_id = e.id
		  AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)
		LEFT JOIN persons m ON m.id = pl.src_person_id
		WHERE (e.id = ANY($1) AND e.entity_type IS DISTINCT FROM 'human')
		   OR e.id IN (
			SELECT dst_person_id FROM person_links
			WHERE src_person_id = ANY($1) AND (valid_to IS NULL OR valid_to >= CURRENT_DATE)
		   )
		GROUP BY e.id, e.own_score`
	rows, err = tx.Query(ctx, aggregates, ids)
	if err != nil {
		return fmt.Errorf("query aggregate inputs: %w", err)
	}
	var entityIDs []int64
	var memberScores []*float64
	var headline []float64
	for rows.Next() {
		var id int64
		var own float64
		var members *float64
		if err := rows.Scan(&id, &own, &members); err != nil {
			rows.Close()
			return fmt.Errorf("scan aggregate inputs: %w", err)
		}
		entityIDs = append(entityIDs, id)
		memberScores = append(memberScores, members)
		headline = append(headline, ComputeAggregateScore(own, members))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read aggregate inputs: %w", err)
	}
	if len(entityIDs) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE persons p SET members_score = u.members, honesty_score = u.headline
		FROM unnest($1::bigint[], $2::float8[], $3::float8[]) AS u(id, members, headline)
		WHERE p.id = u.id`, entityIDs, memberScores, headline)
	if err != nil {
		return fmt.Errorf("update aggregate scores: %w", err)
	}
	return nil
}
