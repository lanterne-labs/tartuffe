package viepublique

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The fetch log follows a merged person and is cleared with a person's facts,
// so the speeches are read again.
func init() {
	core.RequireQuoteForSourceType(sourceType)
	storage.RegisterPersonTable(storage.PersonTable{
		Name:       "viepublique_fetch_log",
		RepointSQL: `UPDATE viepublique_fetch_log SET person_id=$2 WHERE person_id=$1`,
		CleanupSQL: `DELETE FROM viepublique_fetch_log WHERE person_id=$1`,
		ResetSQL:   `DELETE FROM viepublique_fetch_log WHERE person_id=$1`,
	})
}

// Store is the plugin's fetch log: the windows whose speeches were all
// fetched for a person.
type Store struct{ pool *pgxpool.Pool }

// NewStore binds a Store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// LogWindow records that every speech of the person in [from, to) is stored.
func (s *Store) LogWindow(ctx context.Context, personID int64, from, to time.Time, speeches int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO viepublique_fetch_log (person_id, date_from, date_to, speeches)
		VALUES ($1, $2::date, $3::date, $4)`, personID, from, to, speeches)
	if err != nil {
		return fmt.Errorf("logging the vie-publique window %s..%s of person %d: %w", from.Format("2006-01-02"), to.Format("2006-01-02"), personID, err)
	}
	return nil
}

// WindowsCover reports whether every day of [from, to) is in a logged window
// of every one of personIDs.
func (s *Store) WindowsCover(ctx context.Context, personIDs []int64, from, to time.Time) (bool, error) {
	var missing bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM unnest($1::bigint[]) p(id)
			CROSS JOIN generate_series($2::date, $3::date - 1, interval '1 day') d(day)
			WHERE NOT EXISTS (
				SELECT 1 FROM viepublique_fetch_log l
				WHERE l.person_id = p.id AND l.date_from <= d.day::date AND l.date_to > d.day::date))`,
		personIDs, from, to).Scan(&missing)
	if err != nil {
		return false, fmt.Errorf("checking the vie-publique windows %s..%s: %w", from.Format("2006-01-02"), to.Format("2006-01-02"), err)
	}
	return !missing, nil
}
