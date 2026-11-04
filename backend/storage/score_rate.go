package storage

import (
	"context"
	"fmt"
)

// ScoreConfidence says how much a score can be trusted: the contradictions
// its facts would hold at the candidates' average rate, and whether that is
// enough (core.ScoreReliability).
type ScoreConfidence struct {
	ExpectedContradictions float64 `json:"expected_contradictions"`
	ScoreReliable          bool    `json:"score_reliable"`
}

// ContradictionRate is the candidates' average number of contradictions per
// fact: their distinct contradictions (a stance's pairs count once, as in
// the score) over their facts. Detection runs for the candidates only, so
// their rate is the one a person's facts are measured against.
func (s *PostgresStorer) ContradictionRate(ctx context.Context) (float64, error) {
	var facts, bundles int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.fact_count), 0), COALESCE(SUM(b.n), 0)
		FROM persons p
		LEFT JOIN (
			SELECT person_id, COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:' || id::text)) AS n
			FROM contradictions GROUP BY person_id
		) b ON b.person_id = p.id
		WHERE `+CandidateSQL+` AND p.entity_type = 'human'`).Scan(&facts, &bundles)
	if err != nil {
		return 0, fmt.Errorf("contradiction rate: %w", err)
	}
	if facts == 0 {
		return 0, nil
	}
	return float64(bundles) / float64(facts), nil
}
