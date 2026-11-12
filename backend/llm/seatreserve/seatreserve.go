// Package seatreserve keeps the end of a Claude seat's usage for the tasks
// that must always find a model.
//
// The pipeline (extraction, the maintenance pass) uses every seat until its
// limit, and the X bot then has no model to write a tweet or to rewrite one
// when its reader asks (2026-11-12: the 12:00 slot got no draft, the seats
// came back a minute after it). A Reserve watches one seat: once the seat's
// 5 h or 7 d usage reaches Percent, Check gives the time until which the seat
// is to be kept for the reserved tasks (the reset of that window).
//
// The package knows nothing of the endpoint, its seats or its tasks: the
// endpoint asks Check before a call that is not reserved and applies the
// hold itself (llm.ClaudeTeamEndpoint).
//
// The usage of a seat is not in the CLI's replies: it is in the headers of
// every /v1/messages answer (anthropic-ratelimit-unified-5h-utilization and
// -7d-utilization, a fraction of the window, with -reset in Unix seconds). A
// seat token may only run inference, so a reading is a request of one output
// token to the smallest model (Read).
package seatreserve

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// DefaultPercent: the others leave the seat at 90 % of a window.
	DefaultPercent = 90
	// DefaultEvery is the least time between two readings: 4 processes use
	// about 0.5 % of the 5 h window per minute.
	DefaultEvery = time.Minute
	// resetMargin is added to a reset time: the window must have reset when
	// the hold ends.
	resetMargin = time.Minute
	// holdUnknown holds the seat when a window over the line gives no reset
	// time; the next reading decides again.
	holdUnknown = 10 * time.Minute
	readTimeout = 15 * time.Second
)

// Reserve watches the usage of one seat. The zero value keeps no reserve.
type Reserve struct {
	// Percent is the usage of a window from which the seat is kept for the
	// reserved tasks. 0: no reserve, no reading.
	Percent float64
	// Every is the least time between two readings. 0: DefaultEvery.
	Every time.Duration
	// Read reads the usage of the seat of a token. nil: the package's Read.
	Read func(ctx context.Context, token string) (Usage, error)

	mu       sync.Mutex
	last     time.Time // the start of the last reading
	failures int       // the readings that failed in a row
}

// New returns the reserve at percent of a window (0: none).
func New(percent float64) *Reserve {
	return &Reserve{Percent: percent}
}

// Check reads the usage of the seat (name, for the logs; token) when the last
// reading is older than Every, and returns the time until which the seat is
// to be kept for the reserved tasks; zero: no hold (under the line, no
// reading due, or a reading that failed: the seat then serves everyone, as
// without a reserve, and the failure is logged). One reading at a time: a
// call during a reading returns zero at once.
func (r *Reserve) Check(ctx context.Context, name, token string) time.Time {
	if r == nil || r.Percent <= 0 {
		return time.Time{}
	}
	every := r.Every
	if every <= 0 {
		every = DefaultEvery
	}
	now := time.Now()
	r.mu.Lock()
	if now.Sub(r.last) < every {
		r.mu.Unlock()
		return time.Time{}
	}
	r.last = now
	read := r.Read
	r.mu.Unlock()
	if read == nil {
		read = Read
	}
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	usage, err := read(rctx, token)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failures++
		if ctx.Err() == nil && (r.failures == 1 || r.failures%30 == 0) {
			log.Warn().Err(err).Str("seat", name).Int("consecutive", r.failures).
				Msg("seat reserve: reading the usage of the seat failed; no reserve is kept until a reading succeeds")
		}
		return time.Time{}
	}
	r.failures = 0
	until := usage.HoldUntil(r.Percent, now)
	if until.IsZero() {
		return until
	}
	ev := log.Warn().Str("seat", name).Float64("reserve_percent", r.Percent).Time("held_until", until)
	if usage.FiveHour != nil {
		ev = ev.Float64("five_hour_percent", usage.FiveHour.Percent)
	}
	if usage.SevenDay != nil {
		ev = ev.Float64("seven_day_percent", usage.SevenDay.Percent)
	}
	ev.Msg("seat reserve: the seat is at its reserve; only the reserved tasks use it until its window resets")
	return until
}
