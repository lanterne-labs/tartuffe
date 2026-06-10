package storage

import (
	"context"
	"fmt"
)

// VoteCounts holds the community vote totals for a contradiction group.
type VoteCounts struct {
	Agree             int `json:"agree"`
	Disagree          int `json:"disagree"`
	NotAContradiction int `json:"not_a_contradiction"`
}

// UpsertContradictionVote inserts or replaces the vote for (baseFactID, ipHash).
// Returns the updated vote counts for that contradiction group.
func (s *PostgresStorer) UpsertContradictionVote(ctx context.Context, baseFactID int64, ipHash string, vote string) (*VoteCounts, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO contradiction_votes (base_fact_id, ip_hash, vote)
		VALUES ($1, $2, $3)
		ON CONFLICT (base_fact_id, ip_hash) DO UPDATE SET vote = EXCLUDED.vote, created_at = NOW()
	`, baseFactID, ipHash, vote)
	if err != nil {
		return nil, fmt.Errorf("upsert contradiction vote: %w", err)
	}
	return s.getVoteCounts(ctx, baseFactID)
}

// GetVoteCountsForFacts returns vote counts for each base_fact_id in the slice.
// Missing entries (no votes yet) are returned as zero-value VoteCounts.
func (s *PostgresStorer) GetVoteCountsForFacts(ctx context.Context, baseFactIDs []int64) (map[int64]*VoteCounts, error) {
	result := make(map[int64]*VoteCounts, len(baseFactIDs))
	for _, id := range baseFactIDs {
		result[id] = &VoteCounts{}
	}

	if len(baseFactIDs) == 0 {
		return result, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT base_fact_id, vote, COUNT(*) AS cnt
		FROM contradiction_votes
		WHERE base_fact_id = ANY($1)
		GROUP BY base_fact_id, vote
	`, baseFactIDs)
	if err != nil {
		return nil, fmt.Errorf("get vote counts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var v string
		var cnt int
		if err := rows.Scan(&id, &v, &cnt); err != nil {
			return nil, fmt.Errorf("scan vote count: %w", err)
		}
		vc, ok := result[id]
		if !ok {
			vc = &VoteCounts{}
			result[id] = vc
		}
		switch v {
		case "agree":
			vc.Agree = cnt
		case "disagree":
			vc.Disagree = cnt
		case "not_a_contradiction":
			vc.NotAContradiction = cnt
		}
	}
	return result, rows.Err()
}

func (s *PostgresStorer) getVoteCounts(ctx context.Context, baseFactID int64) (*VoteCounts, error) {
	m, err := s.GetVoteCountsForFacts(ctx, []int64{baseFactID})
	if err != nil {
		return nil, err
	}
	vc, ok := m[baseFactID]
	if !ok {
		return &VoteCounts{}, nil
	}
	return vc, nil
}
