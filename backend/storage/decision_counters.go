package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/metrics"
)

// AddDecisionCounts adds the tallies to today's counters in one statement.
func (s *PostgresStorer) AddDecisionCounts(ctx context.Context, counts []metrics.DecisionCount) error {
	if len(counts) == 0 {
		return nil
	}
	stages := make([]string, len(counts))
	outcomes := make([]string, len(counts))
	values := make([]int64, len(counts))
	for i, c := range counts {
		stages[i], outcomes[i], values[i] = c.Stage, c.Outcome, c.Count
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO decision_counters (stage, outcome, day, count)
		SELECT d.stage, d.outcome, CURRENT_DATE, d.count
		FROM unnest($1::text[], $2::text[], $3::bigint[]) AS d(stage, outcome, count)
		ON CONFLICT (stage, outcome, day) DO UPDATE SET count = decision_counters.count + EXCLUDED.count`,
		stages, outcomes, values)
	if err != nil {
		return fmt.Errorf("adding %d decision counts: %w", len(counts), err)
	}
	return nil
}

// DecisionTotals returns the all-time and current-day counts per (stage, outcome).
func (s *PostgresStorer) DecisionTotals(ctx context.Context) (allTime, today []metrics.DecisionCount, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT stage, outcome, SUM(count), SUM(count) FILTER (WHERE day = CURRENT_DATE)
		FROM decision_counters
		GROUP BY stage, outcome
		ORDER BY stage, outcome`)
	if err != nil {
		return nil, nil, fmt.Errorf("reading decision counters: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c metrics.DecisionCount
		var todayCount *int64
		if err := rows.Scan(&c.Stage, &c.Outcome, &c.Count, &todayCount); err != nil {
			return nil, nil, fmt.Errorf("scanning decision counter: %w", err)
		}
		allTime = append(allTime, c)
		if todayCount != nil && *todayCount > 0 {
			today = append(today, metrics.DecisionCount{Stage: c.Stage, Outcome: c.Outcome, Count: *todayCount})
		}
	}
	return allTime, today, rows.Err()
}
