package storage

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// ReviewVerdicts are the verdicts a review gives a contradiction: real (the
// person did contradict themselves), debatable (a reader could argue either
// way), false (no contradiction).
var ReviewVerdicts = []string{"real", "debatable", "false"}

// QualityJob is the job_metrics job_type of the quality gauges.
const QualityJob = "quality"

// ContradictionReview is one verdict on a stored contradiction.
type ContradictionReview struct {
	ContradictionID int64  `json:"id"`
	Verdict         string `json:"verdict"`
	Note            string `json:"note,omitempty"`
}

// RecordContradictionReview stores the verdict of reviewer on a
// contradiction, with the two facts and the reason as they are now. A second
// review of the same contradiction by the same reviewer replaces the first.
func (s *PostgresStorer) RecordContradictionReview(ctx context.Context, reviewer string, r ContradictionReview) error {
	if reviewer == "" {
		return fmt.Errorf("review of contradiction %d: no reviewer", r.ContradictionID)
	}
	if !slices.Contains(ReviewVerdicts, r.Verdict) {
		return fmt.Errorf("review of contradiction %d: verdict %q is not one of %v", r.ContradictionID, r.Verdict, ReviewVerdicts)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO contradiction_reviews (contradiction_id, person_id, person_name, fact_a_id, fact_b_id,
		                                   fact_a_text, fact_b_text, reason, detected_at, verdict, note, reviewer)
		SELECT c.id, c.person_id, p.name, c.fact_a_id, c.fact_b_id,
		       fa.fact_text, fb.fact_text, c.reason, c.detected_at, $2, $3, $4
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		WHERE c.id = $1
		ON CONFLICT (contradiction_id, reviewer) DO UPDATE
		SET verdict = EXCLUDED.verdict, note = EXCLUDED.note,
		    fact_a_text = EXCLUDED.fact_a_text, fact_b_text = EXCLUDED.fact_b_text,
		    reason = EXCLUDED.reason, reviewed_at = NOW()`,
		r.ContradictionID, r.Verdict, r.Note, reviewer)
	if err != nil {
		return fmt.Errorf("review of contradiction %d: %w", r.ContradictionID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("review of contradiction %d: contradiction not found", r.ContradictionID)
	}
	return nil
}

// ListContradictionsToReview returns, in random order, up to limit stored
// contradictions detected at or after since that reviewer has not reviewed
// (limit <= 0 means no cap).
func (s *PostgresStorer) ListContradictionsToReview(ctx context.Context, reviewer string, since time.Time, personID *int64, limit int) ([]ContradictionAuditRow, error) {
	args := []any{reviewer, since}
	where := `WHERE c.detected_at >= $2
		AND NOT EXISTS (SELECT 1 FROM contradiction_reviews r WHERE r.contradiction_id = c.id AND r.reviewer = $1)`
	if personID != nil {
		args = append(args, *personID)
		where += fmt.Sprintf(" AND c.person_id = $%d", len(args))
	}
	order := "ORDER BY RANDOM()"
	if limit > 0 {
		args = append(args, limit)
		order += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return s.listContradictions(ctx, where, order, args)
}

// QualityMetrics measures the state of the stances, the claim groups and the
// contradictions, as job_metrics rows of QualityJob:
//   - facts;
//   - stances, stance_singletons (stances holding one fact), stance_persons;
//   - claim_groups (group parents), claim_group_members;
//   - contradictions, contradiction_bundles (what the score counts),
//     contradiction_persons.
func (s *PostgresStorer) QualityMetrics(ctx context.Context) ([]JobMetric, error) {
	names := []string{
		"facts",
		"stances", "stance_singletons", "stance_persons",
		"claim_groups", "claim_group_members",
		"contradictions", "contradiction_bundles", "contradiction_persons",
	}
	values := make([]int64, len(names))
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM facts),
			(SELECT COUNT(*) FROM stance_documents),
			(SELECT COUNT(*) FROM (SELECT stance_document_id FROM stance_document_facts
			                       GROUP BY 1 HAVING COUNT(*) = 1) one),
			(SELECT COUNT(DISTINCT person_id) FROM stance_documents),
			(SELECT COUNT(DISTINCT general_fact_id) FROM fact_relations WHERE relation_type = 'duplicate'),
			(SELECT COUNT(*) FROM fact_relations WHERE relation_type = 'duplicate'),
			(SELECT COUNT(*) FROM contradictions),
			(SELECT COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:'||id::text)) FROM contradictions),
			(SELECT COUNT(DISTINCT person_id) FROM contradictions)`).
		Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7], &values[8])
	if err != nil {
		return nil, fmt.Errorf("quality metrics: %w", err)
	}
	out := make([]JobMetric, len(names))
	for i, name := range names {
		out[i] = JobMetric{JobType: QualityJob, Metric: name, Value: float64(values[i])}
	}
	return out, nil
}

// RecordQualityMetrics measures QualityMetrics and stores them in job_metrics.
func (s *PostgresStorer) RecordQualityMetrics(ctx context.Context) ([]JobMetric, error) {
	rows, err := s.QualityMetrics(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RecordJobMetrics(ctx, rows); err != nil {
		return nil, fmt.Errorf("recording the quality metrics: %w", err)
	}
	return rows, nil
}
