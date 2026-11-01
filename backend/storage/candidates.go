package storage

import (
	"context"
	"fmt"
)

// The AI work after extraction runs for the CANDIDATES only: the persons
// flagged in persons.attributes ("candidate": true, `dm persons candidate
// add`), the presidential candidates today. The facts of every person are
// still extracted and stored (the votes and transcripts of every deputy, the
// news of whoever the queue asks for); a fact of another person gets no
// grouping gate, no stance, and the maintenance pass (stance refresh, merge,
// fold, pairs, detection, translations) leaves that person out, so no LLM
// call is made for them and no contradiction is detected. The site says so.
//
// CandidateSQL is the predicate, over the persons table aliased p; every
// query that lists the persons of a maintenance step carries it.
const CandidateSQL = `COALESCE(p.attributes->>'candidate', '') = 'true'`

// IsCandidate reports whether the person is flagged as a candidate.
func (s *PostgresStorer) IsCandidate(ctx context.Context, personID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT `+CandidateSQL+` FROM persons p WHERE p.id = $1`, personID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("candidate flag of person %d: %w", personID, err)
	}
	return ok, nil
}

// SetCandidate flags or unflags the person.
func (s *PostgresStorer) SetCandidate(ctx context.Context, personID int64, candidate bool) error {
	var q string
	if candidate {
		q = `UPDATE persons SET attributes = attributes || '{"candidate": true}'::jsonb WHERE id = $1`
	} else {
		q = `UPDATE persons SET attributes = attributes - 'candidate' WHERE id = $1`
	}
	tag, err := s.pool.Exec(ctx, q, personID)
	if err != nil {
		return fmt.Errorf("setting the candidate flag of person %d: %w", personID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("person %d does not exist", personID)
	}
	return nil
}

// ListCandidates returns the flagged persons, by name.
func (s *PostgresStorer) ListCandidates(ctx context.Context) ([]PersonInfo, error) {
	return s.queryPersonInfos(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		WHERE `+CandidateSQL+`
		ORDER BY p.name`)
}
