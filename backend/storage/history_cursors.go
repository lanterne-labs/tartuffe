package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// GetHistoryCursor returns how far back plugin's history has been read;
// ok is false when its history has never been read.
func (s *PostgresStorer) GetHistoryCursor(ctx context.Context, plugin string) (reached time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT reached_at FROM history_cursors WHERE plugin = $1`, plugin).Scan(&reached)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading the history cursor of %s: %w", plugin, err)
	}
	return reached, true, nil
}

// SetHistoryCursor records that plugin's history has been read back to reached.
func (s *PostgresStorer) SetHistoryCursor(ctx context.Context, plugin string, reached time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO history_cursors (plugin, reached_at) VALUES ($1, $2)
		ON CONFLICT (plugin) DO UPDATE SET reached_at = EXCLUDED.reached_at, updated_at = now()`,
		plugin, reached)
	if err != nil {
		return fmt.Errorf("recording the history cursor of %s: %w", plugin, err)
	}
	return nil
}
