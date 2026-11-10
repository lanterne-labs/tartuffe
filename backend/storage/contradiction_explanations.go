package storage

import (
	"context"
	"fmt"
	"time"
)

// ExplainSide is one fact of a contradiction to explain, as the reader sees
// it: its text, the date of its first source, that source and its quote.
type ExplainSide struct {
	FactID    int64
	Text      string
	Date      *time.Time
	Quote     string
	SourceURL string
}

// ContradictionToExplain is a contradiction whose explanation is missing or
// was written from other facts or an older prompt. Hash is what
// SetContradictionExplanation stores with the new text.
type ContradictionToExplain struct {
	ID       int64
	PersonID int64
	Person   string
	Reason   string
	Hash     string
	A, B     ExplainSide
}

// ExplainFilter says which contradictions ListContradictionsToExplain gives.
type ExplainFilter struct {
	// IDs names the contradictions (empty: those of PersonID, or of the
	// candidates). Named contradictions are given whatever their person.
	IDs []int64
	// PersonID restricts to one person (0: the candidates).
	PersonID int64
	// All also gives the contradictions whose explanation is up to date.
	All bool
	// Limit caps the rows (0: no cap).
	Limit int
}

// explainSideLateral is the first source of fact f (alias out): its date, its
// URL, and the first quote any source of the fact holds.
func explainSideLateral(f, out string) string {
	return fmt.Sprintf(`LEFT JOIN LATERAL (
			SELECT MIN(published_at) AS earliest,
			       (array_agg(source_url ORDER BY published_at ASC NULLS LAST))[1] AS url,
			       (array_agg(original_quote ORDER BY published_at ASC NULLS LAST)
			          FILTER (WHERE COALESCE(original_quote, '') <> ''))[1] AS quote
			FROM fact_sources WHERE fact_id = %[1]s.id
		) %[2]s ON true`, f, out)
}

// explanationHashSQL is the hash of what an explanation is written from: the
// prompt version ($1) and the two facts.
const explanationHashSQL = `md5($1::text || ':' || fa.id || ':' || fa.fact_text || ':' || fb.id || ':' || fb.fact_text)`

// ListContradictionsToExplain returns the contradictions to explain under
// version (promptrules.ExplanationVersion), the newest detected first: those
// are the ones the feed shows.
func (s *PostgresStorer) ListContradictionsToExplain(ctx context.Context, version string, f ExplainFilter) ([]ContradictionToExplain, error) {
	ids := f.IDs
	if ids == nil {
		ids = []int64{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, p.id, p.name, COALESCE(c.reason, ''), `+explanationHashSQL+`,
		       fa.id, fa.fact_text, sa.earliest, COALESCE(sa.quote, ''), COALESCE(sa.url, ''),
		       fb.id, fb.fact_text, sb.earliest, COALESCE(sb.quote, ''), COALESCE(sb.url, '')
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		`+explainSideLateral("fa", "sa")+`
		`+explainSideLateral("fb", "sb")+`
		WHERE CASE WHEN cardinality($5::bigint[]) > 0 THEN c.id = ANY($5)
		           WHEN $2::bigint <> 0 THEN c.person_id = $2
		           ELSE `+CandidateSQL+` END
		  AND ($3::boolean OR COALESCE(c.explanation, '') = ''
		       OR c.explanation_hash IS DISTINCT FROM `+explanationHashSQL+`)
		ORDER BY c.detected_at DESC NULLS LAST, c.id DESC
		LIMIT NULLIF($4::int, 0)`, version, f.PersonID, f.All, f.Limit, ids)
	if err != nil {
		return nil, fmt.Errorf("contradictions to explain: %w", err)
	}
	defer rows.Close()
	var out []ContradictionToExplain
	for rows.Next() {
		var c ContradictionToExplain
		if err := rows.Scan(&c.ID, &c.PersonID, &c.Person, &c.Reason, &c.Hash,
			&c.A.FactID, &c.A.Text, &c.A.Date, &c.A.Quote, &c.A.SourceURL,
			&c.B.FactID, &c.B.Text, &c.B.Date, &c.B.Quote, &c.B.SourceURL); err != nil {
			return nil, fmt.Errorf("scan contradiction to explain: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetContradictionExplanation stores the explanation of a contradiction with
// the hash it was written from. The English text is cleared: the translation
// phase writes it again from the new text.
func (s *PostgresStorer) SetContradictionExplanation(ctx context.Context, id int64, explanation, hash string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE contradictions SET explanation = $2, explanation_en = NULL, explanation_hash = $3
		WHERE id = $1`, id, explanation, hash)
	if err != nil {
		return fmt.Errorf("set explanation of contradiction %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("contradiction %d not found when setting its explanation", id)
	}
	return nil
}
