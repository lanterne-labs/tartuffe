// Package seatreserve keeps the end of the last Claude seat that still
// serves for the tasks that must always find a model.
//
// The pipeline (extraction, the maintenance pass) uses every seat until its
// limit, and the X bot then has no model to write a tweet or to rewrite one
// when its reader asks (2026-11-12: the 12:00 slot got no draft, the seats
// came back a minute after it). The pipeline may still use a seat to its
// end, as long as another seat has room: only the last seat with room is
// kept. A Reserve reads the usage of the seats that serve, and once none of
// them is under Percent of its 5 h or 7 d window it names the one to keep
// for the reserved tasks: the one with the most left. The others go on to
// their limit. As soon as a seat has room again (a window reset), nothing
// is kept.
//
// The package knows nothing of the endpoint or its tasks: the endpoint gives
// the seats that serve to Hold before a call that is not reserved and
// applies the answer itself (llm.ClaudeTeamEndpoint).
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
	// DefaultPercent: the last seat with room is left at 90 % of a window.
	DefaultPercent = 90
	// DefaultEvery is the least time between two readings of a seat: 4
	// processes use about 0.5 % of the 5 h window per minute.
	DefaultEvery = time.Minute
	// resetMargin is added to a reset time: the window must have reset when
	// the hold ends.
	resetMargin = time.Minute
	// holdUnknown holds the seat when a window over the line gives no reset
	// time; the next reading decides again.
	holdUnknown = 10 * time.Minute
	readTimeout = 15 * time.Second
)

// Seat is a seat that serves: its name (for the logs) and its token.
type Seat struct {
	Name, Token string
}

// Reserve reads the usage of the seats and names the one to keep. The zero
// value keeps no reserve.
type Reserve struct {
	// Percent is the usage of a window from which a seat has no room. 0: no
	// reserve, no reading.
	Percent float64
	// Every is the least time between two readings of a seat. 0: DefaultEvery.
	Every time.Duration
	// Read reads the usage of the seat of a token. nil: the package's Read.
	Read func(ctx context.Context, token string) (Usage, error)

	mu       sync.Mutex
	readings map[string]*reading // by seat name
	kept     string              // the seat kept at the last answer, for the logs
}

// reading is what is known of a seat: its last usage (ok: one was read), the
// start of the last reading, and the readings that failed in a row.
type reading struct {
	usage    Usage
	ok       bool
	started  time.Time
	failures int
}

// New returns the reserve at percent of a window (0: none).
func New(percent float64) *Reserve {
	return &Reserve{Percent: percent}
}

// Hold returns the seat to keep for the reserved tasks among the seats that
// serve, and until when; "" when a seat still has room, when the usage of a
// seat is not known (a reading failed: nothing is kept, as without a
// reserve, and the failure is logged), or without a reserve. It first reads
// the seats whose reading is older than Every, all at once.
func (r *Reserve) Hold(ctx context.Context, serving []Seat) (name string, until time.Time) {
	if r == nil || r.Percent <= 0 || len(serving) == 0 {
		return "", time.Time{}
	}
	r.refresh(ctx, serving)
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	var left float64
	for _, s := range serving {
		rd := r.readings[s.Name]
		if rd == nil || !rd.ok {
			name = ""
			break
		}
		end := rd.usage.HoldUntil(r.Percent, now)
		if end.IsZero() {
			name = "" // this seat has room: the pipeline uses the others to their end
			break
		}
		if room := 100 - rd.usage.Used(); name == "" || room > left {
			name, until, left = s.Name, end, room
		}
	}
	if name == "" {
		until = time.Time{}
	}
	if name != r.kept {
		r.kept = name
		if name == "" {
			log.Info().Msg("seat reserve: a seat has room again; no seat is kept")
		} else {
			log.Warn().Str("seat", name).Float64("reserve_percent", r.Percent).Float64("used_percent", 100-left).Time("held_until", until).
				Msg("seat reserve: no other seat has room; only the reserved tasks use this seat until its window resets")
		}
	}
	return name, until
}

// refresh reads the seats whose reading is due, at once, and stores the
// results. A seat being read by another call is not read again.
func (r *Reserve) refresh(ctx context.Context, serving []Seat) {
	every := r.Every
	if every <= 0 {
		every = DefaultEvery
	}
	read := r.Read
	if read == nil {
		read = Read
	}
	now := time.Now()
	var due []Seat
	r.mu.Lock()
	if r.readings == nil {
		r.readings = map[string]*reading{}
	}
	for _, s := range serving {
		rd := r.readings[s.Name]
		if rd == nil {
			rd = &reading{}
			r.readings[s.Name] = rd
		}
		if now.Sub(rd.started) >= every {
			rd.started = now
			due = append(due, s)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, readTimeout)
			defer cancel()
			usage, err := read(rctx, s.Token)
			r.mu.Lock()
			defer r.mu.Unlock()
			rd := r.readings[s.Name]
			if err != nil {
				rd.ok = false
				rd.failures++
				if ctx.Err() == nil && (rd.failures == 1 || rd.failures%30 == 0) {
					log.Warn().Err(err).Str("seat", s.Name).Int("consecutive", rd.failures).
						Msg("seat reserve: reading the usage of the seat failed; no reserve is kept until a reading succeeds")
				}
				return
			}
			rd.usage, rd.ok, rd.failures = usage, true, 0
		}()
	}
	wg.Wait()
}
