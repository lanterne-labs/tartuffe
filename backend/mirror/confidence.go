package mirror

import (
	"context"
	"math"
	"sync"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

// baselineTTL is how long the candidates' score baseline is kept: it moves
// with detection and collection, over hours.
const baselineTTL = 5 * time.Minute

// baselineCache keeps the candidates' score baseline between requests.
type baselineCache struct {
	mu   sync.Mutex
	base core.ScoreBaseline
	at   time.Time
}

// scoreBaseline returns the candidates' score baseline, measured again past
// baselineTTL.
func (s *Service) scoreBaseline(ctx context.Context) (core.ScoreBaseline, error) {
	s.baseline.mu.Lock()
	defer s.baseline.mu.Unlock()
	if !s.baseline.at.IsZero() && time.Since(s.baseline.at) < baselineTTL {
		return s.baseline.base, nil
	}
	base, err := s.store.ScoreBaseline(ctx)
	if err != nil {
		return core.ScoreBaseline{}, err
	}
	s.baseline.base, s.baseline.at = base, time.Now()
	return base, nil
}

// confidence says whether the score honesty, built on facts, is reliable.
func confidence(facts int, honesty float64, base core.ScoreBaseline) storage.ScoreConfidence {
	return storage.ScoreConfidence{
		CoveredFacts:  int(math.Round(base.CoveredFacts)),
		ScoreReliable: base.ScoreReliability(facts, honesty),
	}
}
