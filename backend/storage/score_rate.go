package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"

	"github.com/jackc/pgx/v5"
)

// rowQuerier is a transaction or the pool.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ScoreRate is the candidates' distinct contradictions per fact, the rate a
// person's contradictions are compared with in the score (core.HonestyExcess):
// the Rate of ScoreBaseline, 0 while the candidates hold no contradiction.
func (s *PostgresStorer) ScoreRate(ctx context.Context) (float64, error) {
	return scoreRate(ctx, s.pool)
}

// scoreRate reads the rate within q, so a score written in a transaction
// counts the contradiction the transaction just added.
func scoreRate(ctx context.Context, q rowQuerier) (float64, error) {
	var facts, bundles float64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.fact_count), 0)::float8, COALESCE(SUM(b.n), 0)::float8
		FROM persons p
		LEFT JOIN (
			SELECT person_id, COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:' || id::text)) AS n
			FROM contradictions GROUP BY person_id
		) b ON b.person_id = p.id
		WHERE `+CandidateSQL+` AND p.entity_type = 'human'`).Scan(&facts, &bundles)
	if err != nil {
		return 0, fmt.Errorf("score rate: %w", err)
	}
	if facts == 0 {
		return 0, nil
	}
	return bundles / facts, nil
}

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
