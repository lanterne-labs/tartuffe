package mirror

import (
	"context"
	"sync"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

// rateTTL is how long the candidates' contradiction rate is kept: it moves
// with detection, over hours.
const rateTTL = 5 * time.Minute

// rateCache keeps the candidates' contradiction rate between requests.
type rateCache struct {
	mu   sync.Mutex
	rate float64
	at   time.Time
}

// contradictionRate returns the candidates' contradictions per fact, read
// again past rateTTL.
func (s *Service) contradictionRate(ctx context.Context) (float64, error) {
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	if !s.rate.at.IsZero() && time.Since(s.rate.at) < rateTTL {
		return s.rate.rate, nil
	}
	rate, err := s.store.ContradictionRate(ctx)
	if err != nil {
		return 0, err
	}
	s.rate.rate, s.rate.at = rate, time.Now()
	return rate, nil
}

// confidence is the confidence of a score built on facts at rate.
func confidence(facts int, rate float64) storage.ScoreConfidence {
	expected, reliable := core.ScoreReliability(facts, rate)
	return storage.ScoreConfidence{ExpectedContradictions: expected, ScoreReliable: reliable}
}
