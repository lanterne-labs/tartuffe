package core

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend/llm"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// OccurrenceRuleHash identifies the grouping gate's prompt: a verdict stored
// under another hash was given by another rule and is asked again.
func OccurrenceRuleHash() string {
	h := sha256.Sum256([]byte(buildOccurrencePrompt(ClaimEvidence{Text: "\x00"}, ClaimEvidence{Text: "\x00"})))
	return hex.EncodeToString(h[:])[:16]
}

// String is the stored form of a verdict.
func (v OccurrenceVerdict) String() string {
	switch v {
	case OccurrenceSame:
		return "same"
	case OccurrenceContained:
		return "bundle"
	default:
		return "different"
	}
}

// ParseOccurrenceVerdict reads a stored verdict.
func ParseOccurrenceVerdict(s string) (OccurrenceVerdict, error) {
	switch s {
	case "same":
		return OccurrenceSame, nil
	case "bundle":
		return OccurrenceContained, nil
	case "different":
		return OccurrenceDifferent, nil
	}
	return OccurrenceDifferent, fmt.Errorf("unknown occurrence verdict %q", s)
}

// OccurrenceCheckStorer keeps the gate's verdicts on fact pairs. The
// insertion compares a new fact with its 5 nearest groups only; the fold asks
// the pairs no verdict covers and reuses the others.
type OccurrenceCheckStorer interface {
	// LoadOccurrenceChecks returns the verdicts stored under ruleHash for
	// pairs (OrderedPair).
	LoadOccurrenceChecks(ctx context.Context, pairs [][2]int64, ruleHash string) (map[[2]int64]OccurrenceVerdict, error)
	// RecordOccurrenceCheck stores the verdict on the pair a, b under ruleHash.
	RecordOccurrenceCheck(ctx context.Context, a, b int64, ruleHash string, v OccurrenceVerdict) error
}

// FoldSweepStorer is the storage view of the fold sweep of the maintenance.
type FoldSweepStorer interface {
	FactFoldStorer
	// ListPersonsNeedingFold returns the persons with at least two active
	// facts whose active facts changed since their last fold under ruleHash.
	ListPersonsNeedingFold(ctx context.Context, ruleHash string) ([]PersonRecord, error)
	// RecordFold memoizes the fold of the person's current active facts.
	RecordFold(ctx context.Context, personID int64, ruleHash string) error
}

// FoldSweepStats counts what a fold sweep did.
type FoldSweepStats struct {
	Persons, Clusters, Folded int
	Asked                     int64
	Pending                   int
}

// RunFoldSweep groups the split occurrences of the persons whose active
// facts changed since their last fold: two groups of one claim that the
// insertion never compared (Glucksmann's primary win stood in 6 groups, and
// each made its own contradiction rows). The persons run as many at once as
// the endpoint serves; no new person starts after maxCalls gate calls (0: no
// cap), and the persons left stay pending. A person whose fold made no gate
// error is memoized. An unavailable LLM stops the sweep.
func RunFoldSweep(ctx context.Context, storer FoldSweepStorer, judge SameOccurrenceJudge, opts FoldOptions, maxCalls int64) (FoldSweepStats, error) {
	var stats FoldSweepStats
	ruleHash := OccurrenceRuleHash()
	persons, err := storer.ListPersonsNeedingFold(ctx, ruleHash)
	if err != nil {
		return stats, fmt.Errorf("listing the persons to fold: %w", err)
	}
	stats.Pending = len(persons)
	var asked atomic.Int64
	opts.Asked = &asked
	var mu sync.Mutex
	err = llm.ForEach(ctx, persons, func(ctx context.Context, p PersonRecord) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("fold sweep stopped: %w", err)
		}
		if maxCalls > 0 && asked.Load() >= maxCalls {
			return nil
		}
		clusters, err := FoldClustersForPerson(ctx, storer, judge, p, opts)
		if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
			return fmt.Errorf("fold sweep stopped at %s: %w", p.Name, errors.Join(err, ctx.Err()))
		}
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("Fold sweep: the person failed (continuing)")
			return nil
		}
		folded := 0
		for _, c := range clusters {
			if _, err := ApplyFoldCluster(ctx, storer, p, c); err != nil {
				log.Error().Err(err).Str("person", p.Name).Msg("Fold sweep: grouping a cluster failed")
				continue
			}
			folded++
		}
		if folded == len(clusters) {
			if err := storer.RecordFold(ctx, p.ID, ruleHash); err != nil {
				log.Error().Err(err).Str("person", p.Name).Msg("Fold sweep: recording the fold failed")
			}
		}
		mu.Lock()
		stats.Persons++
		stats.Clusters += len(clusters)
		stats.Folded += folded
		mu.Unlock()
		return nil
	})
	stats.Asked = asked.Load()
	log.Info().Int("pending", stats.Pending).Int("persons", stats.Persons).Int("clusters", stats.Clusters).
		Int("folded", stats.Folded).Int64("gate_calls", stats.Asked).Msg("Fold sweep finished")
	return stats, err
}
