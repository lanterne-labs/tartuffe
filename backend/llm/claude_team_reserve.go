package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// The reserve of the last seat.
//
// The pipeline (extraction, the maintenance pass) uses every seat until its
// limit, and the X bot then has no model to write a tweet or to rewrite one
// when its reader asks (2026-11-12: the 12:00 slot got no draft, the seats
// came back a minute after it). So the last seat of the list keeps the end of
// its usage for the reserved tasks: once its 5 h or 7 d usage reaches
// ReservePercent, the other tasks see the seat as down until that window
// resets. The reserved tasks use any seat that serves, the first of the list
// first, so they take from the reserve only when the other seats are down.
//
// The usage of a seat is not in the CLI's replies: it is in the headers of
// every /v1/messages answer (anthropic-ratelimit-unified-5h-utilization and
// -7d-utilization, a fraction of the window, with -reset in Unix seconds). A
// seat token may only run inference, so the reading is a request of one
// output token to the smallest model, at most once per reserveProbeEvery
// while the pipeline calls.

// reservedTasks are the tasks the reserve is kept for.
var reservedTasks = map[Task]bool{TaskTweet: true}

const (
	// defaultReservePercent: the pipeline leaves the last seat at 90 % of a
	// window (CLAUDE_TEAM_RESERVE_PERCENT; 0: no reserve).
	defaultReservePercent = 90
	// reserveProbeEvery is the least time between two readings: 4 processes
	// use about 0.5 % of the 5 h window per minute.
	reserveProbeEvery = time.Minute
	reserveProbeModel = "claude-haiku-5-5"
	// The API accepts a seat token only with the CLI's system prompt.
	reserveProbeSystem = "You are Claude Code, Anthropic's official CLI for Claude."
	// reserveHoldUnknown holds the seat when a window over the line gives no
	// reset time; the next reading decides again.
	reserveHoldUnknown = 10 * time.Minute
)

var reserveProbeURL = "https://api.anthropic.com/v1/messages"

// usageWindow is one usage window of a seat: the percentage used and when it
// resets (zero: not given).
type usageWindow struct {
	Percent float64
	Reset   time.Time
}

// seatUsage is the usage of a seat; a window the headers do not give is nil.
type seatUsage struct {
	FiveHour, SevenDay *usageWindow
}

// holdUntil returns the time until which a seat at this usage is kept for the
// reserved tasks: the latest reset of the windows at or over percent (now +
// reserveHoldUnknown for a window without a reset); zero when no window is.
func (u seatUsage) holdUntil(percent float64, now time.Time) time.Time {
	var until time.Time
	for _, w := range []*usageWindow{u.FiveHour, u.SevenDay} {
		if w == nil || w.Percent < percent {
			continue
		}
		end := w.Reset.Add(claudeResetMargin)
		if w.Reset.IsZero() || !end.After(now) {
			end = now.Add(reserveHoldUnknown)
		}
		if end.After(until) {
			until = end
		}
	}
	return until
}

// checkReserve reads the usage of the reserved seat when the last reading is
// older than reserveProbeEvery, and holds the seat for the reserved tasks
// when a window is at ReservePercent. One reading at a time: the other calls
// go on with what is known. A reading that fails changes nothing (the seat
// serves everyone, as without a reserve) and is logged.
func (t *ClaudeTeamEndpoint) checkReserve(ctx context.Context) {
	if t.ReservePercent <= 0 || len(t.seats) == 0 {
		return
	}
	i := len(t.seats) - 1
	now := time.Now()
	t.mu.Lock()
	s := &t.seats[i]
	if now.Sub(t.probed) < reserveProbeEvery || !s.blocked(now, false).IsZero() {
		t.mu.Unlock()
		return
	}
	t.probed = now
	name, token := s.name, s.token
	probe := t.probe
	t.mu.Unlock()
	if probe == nil {
		probe = probeSeatUsage
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	usage, err := probe(pctx, token)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.probeFailures++
		if ctx.Err() == nil && (t.probeFailures == 1 || t.probeFailures%30 == 0) {
			log.Warn().Err(err).Str("seat", name).Int("consecutive", t.probeFailures).
				Msg("claude seat: reading the usage of the reserved seat failed; no reserve is kept until a reading succeeds")
		}
		return
	}
	t.probeFailures = 0
	until := usage.holdUntil(t.ReservePercent, now)
	if until.IsZero() {
		return
	}
	t.seats[i].held = until
	t.wakeLocked()
	ev := log.Warn().Str("seat", name).Float64("reserve_percent", t.ReservePercent).Time("held_until", until)
	if usage.FiveHour != nil {
		ev = ev.Float64("five_hour_percent", usage.FiveHour.Percent)
	}
	if usage.SevenDay != nil {
		ev = ev.Float64("seven_day_percent", usage.SevenDay.Percent)
	}
	ev.Msg("claude seat: the reserved seat is at its reserve; only the reserved tasks (the X bot) use it until its window resets")
}

// probeSeatUsage reads the usage of the seat of token from the headers of a
// one-token request. A 429 also carries them: a seat at its limit reads as
// such. The token is never in an error.
func probeSeatUsage(ctx context.Context, token string) (seatUsage, error) {
	body, err := json.Marshal(map[string]any{
		"model":      reserveProbeModel,
		"max_tokens": 1,
		"system":     reserveProbeSystem,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		return seatUsage{}, fmt.Errorf("usage probe: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reserveProbeURL, bytes.NewReader(body))
	if err != nil {
		return seatUsage{}, fmt.Errorf("usage probe: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return seatUsage{}, fmt.Errorf("usage probe: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	usage := seatUsageFromHeaders(resp.Header)
	if usage.FiveHour == nil && usage.SevenDay == nil {
		return seatUsage{}, fmt.Errorf("usage probe: HTTP %d without usage headers", resp.StatusCode)
	}
	return usage, nil
}

// seatUsageFromHeaders reads the unified rate-limit headers.
func seatUsageFromHeaders(h http.Header) seatUsage {
	window := func(name string) *usageWindow {
		f, err := strconv.ParseFloat(h.Get("anthropic-ratelimit-unified-"+name+"-utilization"), 64)
		if err != nil || f < 0 {
			return nil
		}
		w := &usageWindow{Percent: f * 100}
		if sec, err := strconv.ParseInt(h.Get("anthropic-ratelimit-unified-"+name+"-reset"), 10, 64); err == nil && sec > 0 {
			w.Reset = time.Unix(sec, 0)
		}
		return w
	}
	return seatUsage{FiveHour: window("5h"), SevenDay: window("7d")}
}
