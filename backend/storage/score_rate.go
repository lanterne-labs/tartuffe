package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
)

// ScoreConfidence says whether a score is reliable (core.ScoreReliability)
// and from how many facts a person is covered (the same for everyone, from
// the candidates' average rate).
type ScoreConfidence struct {
	CoveredFacts  int  `json:"covered_facts"`
	ScoreReliable bool `json:"score_reliable"`
}

// ScoreBaseline measures the candidates' facts, distinct contradictions (a
// stance's pairs count once, as in the score) and honesty scores:
// core.NewScoreBaseline. Detection runs for the candidates only, so they are
// the ones a person's score is measured against.
func (s *PostgresStorer) ScoreBaseline(ctx context.Context) (core.ScoreBaseline, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.fact_count, COALESCE(b.n, 0), p.honesty_score
		FROM persons p
		LEFT JOIN (
			SELECT person_id, COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:' || id::text)) AS n
			FROM contradictions GROUP BY person_id
		) b ON b.person_id = p.id
		WHERE `+CandidateSQL+` AND p.entity_type = 'human'`)
	if err != nil {
		return core.ScoreBaseline{}, fmt.Errorf("score baseline: %w", err)
	}
	defer rows.Close()
	var facts, bundles []int
	var honesty []float64
	for rows.Next() {
		var f, b int
		var h float64
		if err := rows.Scan(&f, &b, &h); err != nil {
			return core.ScoreBaseline{}, fmt.Errorf("scan score baseline: %w", err)
		}
		facts, bundles, honesty = append(facts, f), append(bundles, b), append(honesty, h)
	}
	if err := rows.Err(); err != nil {
		return core.ScoreBaseline{}, fmt.Errorf("score baseline rows: %w", err)
	}
	return core.NewScoreBaseline(facts, bundles, honesty), nil
}
