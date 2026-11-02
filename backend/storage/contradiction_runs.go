package storage

import (
	"context"
	"digital-mirror/backend/metrics"
	"fmt"
	"time"
)

// NeighborPair is one candidate pair of contradiction detection as
// GetNeighborPairsForPerson returns it: the neighbor's L2 distance and verb
// feed the paraphrase gate; Target* is the partner the pair is judged
// against, the neighbor's most general implication ancestor when it has one,
// the neighbor itself otherwise.
type NeighborPair struct {
	FactID       int64
	NeighborID   int64
	NeighborVerb string
	Distance     float64 // 0 in check-all mode
	TargetID     int64
	TargetText   string
	TargetDate   *time.Time // Earliest publication date of the target's sources
}

// knnNeighborsSQL pairs every active fact of the person with its 30 nearest
// active facts within embeddingSimilarityThreshold ($2), the
// FindNeighborFactsForPerson walk, kept when the neighbor id is the greater
// of the two.
const knnNeighborsSQL = `
		neighbors AS (
			SELECT a.id AS fact_id, n.id AS neighbor_id, n.fact_text, n.canonical_verb, n.distance
			FROM person_facts a
			CROSS JOIN LATERAL (
				SELECT b.id, b.fact_text, b.canonical_verb, b.embedding <-> a.embedding AS distance
				FROM person_facts b
				WHERE b.id <> a.id
				  AND b.embedding <-> a.embedding <= $2
				ORDER BY b.embedding <-> a.embedding
				LIMIT 30
			) n
			WHERE n.id > a.id
		)`

// allNeighborsSQL pairs every active fact of the person with every other one
// (check-all mode); no distance is computed.
const allNeighborsSQL = `
		neighbors AS (
			SELECT a.id AS fact_id, b.id AS neighbor_id, b.fact_text, b.canonical_verb, 0::float8 AS distance
			FROM person_facts a
			JOIN person_facts b ON b.id > a.id
		)`

// GetNeighborPairsForPerson runs the whole candidate walk of contradiction
// detection for one person in a single statement: every active fact paired
// with its KNN neighbors (or with every other active fact when checkAll),
// each partner replaced by its most general implication ancestor
// (fact_relations 'implication' edges walked up to depth 10, the deepest
// wins) resolved to id, text and earliest publication date. Ordered by fact
// id, then distance, then neighbor id.
func (s *PostgresStorer) GetNeighborPairsForPerson(ctx context.Context, personID int64, checkAll bool) ([]NeighborPair, error) {
	timer := metrics.NewQueryTimer("neighbor_pairs_for_person")
	start := time.Now()

	neighborsSQL := knnNeighborsSQL
	args := []any{personID, embeddingSimilarityThreshold}
	if checkAll {
		neighborsSQL = allNeighborsSQL
		args = args[:1]
	}
	query := `
		WITH RECURSIVE person_facts AS (
			SELECT f.id, f.fact_text, f.embedding, COALESCE(f.canonical_verb, '') AS canonical_verb
			FROM facts f
			JOIN (
				SELECT DISTINCT fs.fact_id
				FROM fact_mentions fm
				JOIN fact_sources fs ON fm.fact_source_id = fs.id
				WHERE fm.person_id = $1
			) pf ON pf.fact_id = f.id
			WHERE NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			)
		),` + neighborsSQL + `,
		ancestors AS (
			SELECT DISTINCT neighbor_id AS start_id, neighbor_id AS fact_id, 0 AS depth
			FROM neighbors
			UNION ALL
			SELECT anc.start_id, fr.general_fact_id, anc.depth + 1
			FROM fact_relations fr
			JOIN ancestors anc ON fr.specific_fact_id = anc.fact_id
			WHERE fr.relation_type = 'implication' AND anc.depth < 10
		),
		top_ancestor AS (
			SELECT DISTINCT ON (start_id) start_id, fact_id
			FROM ancestors
			ORDER BY start_id, depth DESC, fact_id
		)
		SELECT nb.fact_id, nb.neighbor_id, nb.canonical_verb, nb.distance,
		       COALESCE(af.id, nb.neighbor_id) AS target_id,
		       COALESCE(af.fact_text, nb.fact_text) AS target_text,
		       (SELECT MIN(fs.published_at) FROM fact_sources fs
		        WHERE fs.fact_id = COALESCE(af.id, nb.neighbor_id)) AS target_date,
		       tf.statement_date::timestamp AT TIME ZONE 'UTC', tf.statement_date_precision
		FROM neighbors nb
		JOIN top_ancestor ta ON ta.start_id = nb.neighbor_id
		LEFT JOIN facts af ON af.id = ta.fact_id AND ta.fact_id <> nb.neighbor_id
		JOIN facts tf ON tf.id = COALESCE(af.id, nb.neighbor_id)
		ORDER BY nb.fact_id, nb.distance, nb.neighbor_id`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		timer.ObserveDuration(err)
		return nil, fmt.Errorf("failed to query neighbor pairs for person %d: %w", personID, err)
	}
	defer rows.Close()

	var results []NeighborPair
	for rows.Next() {
		var p NeighborPair
		var sd statementDate
		if err := rows.Scan(&p.FactID, &p.NeighborID, &p.NeighborVerb, &p.Distance, &p.TargetID, &p.TargetText, &p.TargetDate, &sd.date, &sd.precision); err != nil {
			timer.ObserveDuration(err)
			return nil, fmt.Errorf("failed to scan neighbor pair for person %d: %w", personID, err)
		}
		p.TargetDate = sd.of(p.TargetDate)
		results = append(results, p)
	}

	err = rows.Err()
	timer.ObserveDuration(err)
	metrics.ObserveVectorSearch(time.Since(start), len(results))

	if err != nil {
		return nil, fmt.Errorf("error iterating neighbor pairs for person %d: %w", personID, err)
	}
	return results, nil
}
