package worker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
)

// defaultExtractionShare is the share of what is left of the API key's day
// that a round of extraction may spend (LLM_EXTRACTION_SHARE).
const defaultExtractionShare = 0.6

// extractionShareFromEnv reads LLM_EXTRACTION_SHARE, a number from 0 to 1;
// 0 turns the split off.
func extractionShareFromEnv() (float64, error) {
	s := strings.TrimSpace(os.Getenv("LLM_EXTRACTION_SHARE"))
	if s == "" {
		return defaultExtractionShare, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1 {
		return 0, fmt.Errorf("LLM_EXTRACTION_SHARE %q: want a number from 0 to 1", s)
	}
	return v, nil
}

// rejudgeBudgetFromEnv reads DETECT_REJUDGE_BUDGET: how many stale negative
// pair verdicts (a NO under an older rulebook hash) a maintenance pass
// re-opens for the detector. Not set: defaultRejudgeBudget, none. A rulebook
// change re-judged every NO ever recorded (13,987 pairs after the review of
// 2026-11-08, 35,000 judged on 2026-11-05), while most changes add rules
// against false positives, which can only turn a YES into a NO: the re-judge
// is asked for, on the worker service, when a change is meant to find more.
func rejudgeBudgetFromEnv() (int, error) {
	s := strings.TrimSpace(os.Getenv("DETECT_REJUDGE_BUDGET"))
	if s == "" {
		return defaultRejudgeBudget, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("DETECT_REJUDGE_BUDGET %q: want a whole number, 0 or more", s)
	}
	return v, nil
}

// spendSplit shares the API key's day between extraction and the rest of the
// pipeline, in rounds. A round of extraction may spend share of what is left;
// then the maintenance pass runs; what is left after it starts the next round.
// While the seats serve the API key is not paying, and the worker keeps its
// usual order.
type spendSplit struct {
	share float64

	mu        sync.Mutex
	day       time.Time
	threshold float64
	started   bool
}

// reached reports whether the round of extraction has spent its share, given
// the spend and the cap of the day at now.
func (b *spendSplit) reached(now time.Time, spent, cap float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	day := now.UTC().Truncate(24 * time.Hour)
	if !b.started || !day.Equal(b.day) {
		b.startLocked(day, spent, cap)
	}
	return spent >= b.threshold
}

// nextRound starts a round over what is left of the day.
func (b *spendSplit) nextRound(now time.Time, spent, cap float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startLocked(now.UTC().Truncate(24*time.Hour), spent, cap)
}

func (b *spendSplit) startLocked(day time.Time, spent, cap float64) {
	b.day, b.started = day, true
	b.threshold = spent + b.share*max(cap-spent, 0)
}

// apiPays returns the spend and the cap of the day when the split applies: the
// share is set, the key has a cap, and every seat is at its limit.
func (h *Housekeeping) apiPays(ctx context.Context) (spent, cap float64, pays bool, err error) {
	if h.split == nil || h.split.share <= 0 {
		return 0, 0, false, nil
	}
	serving := llm.SeatsServing
	if h.seatsServing != nil {
		serving = h.seatsServing
	}
	if serving() {
		return 0, 0, false, nil
	}
	read := h.readSpend
	if read == nil {
		read = func(ctx context.Context, day time.Time) (float64, float64, error) {
			c, err := llm.APIDayCapUSD()
			if err != nil {
				return 0, 0, err
			}
			s, err := storage.NewAPISpendLedger(h.Storer).Spent(ctx, day)
			return s, c, err
		}
	}
	spent, cap, err = read(ctx, h.now().UTC().Truncate(24*time.Hour))
	if err != nil {
		return 0, 0, false, fmt.Errorf("the spend of the day: %w", err)
	}
	return spent, cap, cap > 0, nil
}
