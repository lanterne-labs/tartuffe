package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APISpendLedger is the llm.SpendLedger of the database: one row of
// llm_api_spend per UTC day, shared by every process.
type APISpendLedger struct{ pool *pgxpool.Pool }

// NewAPISpendLedger returns the ledger over the database of s.
func NewAPISpendLedger(s *PostgresStorer) *APISpendLedger {
	return &APISpendLedger{pool: s.Pool()}
}

// Reserve adds cost to the spend of day in one statement, so two processes
// never pass limit together; it returns the spend of the day and whether
// cost was added.
func (l *APISpendLedger) Reserve(ctx context.Context, day time.Time, cost, limit float64) (float64, bool, error) {
	if cost > limit {
		return 0, false, nil
	}
	var spent float64
	err := l.pool.QueryRow(ctx, `
		INSERT INTO llm_api_spend (day, spent_usd) VALUES ($1, $2)
		ON CONFLICT (day) DO UPDATE SET spent_usd = llm_api_spend.spent_usd + EXCLUDED.spent_usd, updated_at = NOW()
		WHERE llm_api_spend.spent_usd + EXCLUDED.spent_usd <= $3
		RETURNING spent_usd`, day.UTC(), cost, limit).Scan(&spent)
	if errors.Is(err, pgx.ErrNoRows) {
		err = l.pool.QueryRow(ctx, `SELECT spent_usd FROM llm_api_spend WHERE day = $1`,
			day.UTC()).Scan(&spent)
		if err != nil {
			return 0, false, fmt.Errorf("read the api spend of the day: %w", err)
		}
		return spent, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reserve api spend: %w", err)
	}
	return spent, true, nil
}

// Settle adds delta to the spend of day.
func (l *APISpendLedger) Settle(ctx context.Context, day time.Time, delta float64) error {
	_, err := l.pool.Exec(ctx, `
		UPDATE llm_api_spend SET spent_usd = GREATEST(spent_usd + $2, 0), updated_at = NOW() WHERE day = $1`,
		day.UTC(), delta)
	if err != nil {
		return fmt.Errorf("settle api spend: %w", err)
	}
	return nil
}

// AddPhase adds the real cost of a call to its phase of the day
// (llm.PhaseLedger).
func (l *APISpendLedger) AddPhase(ctx context.Context, day time.Time, phase string, cost float64) error {
	_, err := l.pool.Exec(ctx, `
		INSERT INTO llm_api_spend_phase (day, phase, spent_usd) VALUES ($1, $2, $3)
		ON CONFLICT (day, phase) DO UPDATE SET spent_usd = llm_api_spend_phase.spent_usd + EXCLUDED.spent_usd, updated_at = NOW()`,
		day.UTC(), phase, cost)
	if err != nil {
		return fmt.Errorf("api spend phase: %w", err)
	}
	return nil
}

// Spent returns the spend of day, reservations included, as the cap counts it.
func (l *APISpendLedger) Spent(ctx context.Context, day time.Time) (float64, error) {
	var spent float64
	err := l.pool.QueryRow(ctx, `SELECT spent_usd FROM llm_api_spend WHERE day = $1`, day.UTC()).Scan(&spent)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("api spend: %w", err)
	}
	return spent, nil
}
