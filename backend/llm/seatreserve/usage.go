package seatreserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Window is one usage window of a seat: the percentage used and when it
// resets (zero: not given).
type Window struct {
	Percent float64
	Reset   time.Time
}

// Usage is the usage of a seat; a window the headers do not give is nil.
type Usage struct {
	FiveHour, SevenDay *Window
}

// HoldUntil returns the time until which a seat at this usage is kept for the
// reserved tasks: the latest reset of the windows at or over percent (now +
// holdUnknown for a window without a reset); zero when no window is.
func (u Usage) HoldUntil(percent float64, now time.Time) time.Time {
	var until time.Time
	for _, w := range []*Window{u.FiveHour, u.SevenDay} {
		if w == nil || w.Percent < percent {
			continue
		}
		end := w.Reset.Add(resetMargin)
		if w.Reset.IsZero() || !end.After(now) {
			end = now.Add(holdUnknown)
		}
		if end.After(until) {
			until = end
		}
	}
	return until
}

const (
	readModel = "claude-haiku-5-5"
	// The API accepts a seat token only with the CLI's system prompt.
	readSystem = "You are Claude Code, Anthropic's official CLI for Claude."
)

// readURL is the endpoint of a reading; the tests point it at their server.
var readURL = "https://api.anthropic.com/v1/messages"

// Read reads the usage of the seat of token from the headers of a one-token
// request. A 429 also carries them: a seat at its limit reads as such. The
// token is never in an error.
func Read(ctx context.Context, token string) (Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model":      readModel,
		"max_tokens": 1,
		"system":     readSystem,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		return Usage{}, fmt.Errorf("seat usage: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, readURL, bytes.NewReader(body))
	if err != nil {
		return Usage{}, fmt.Errorf("seat usage: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("seat usage: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	usage := FromHeaders(resp.Header)
	if usage.FiveHour == nil && usage.SevenDay == nil {
		return Usage{}, fmt.Errorf("seat usage: HTTP %d without usage headers", resp.StatusCode)
	}
	return usage, nil
}

// FromHeaders reads the unified rate-limit headers of an answer.
func FromHeaders(h http.Header) Usage {
	window := func(name string) *Window {
		f, err := strconv.ParseFloat(h.Get("anthropic-ratelimit-unified-"+name+"-utilization"), 64)
		if err != nil || f < 0 {
			return nil
		}
		w := &Window{Percent: f * 100}
		if sec, err := strconv.ParseInt(h.Get("anthropic-ratelimit-unified-"+name+"-reset"), 10, 64); err == nil && sec > 0 {
			w.Reset = time.Unix(sec, 0)
		}
		return w
	}
	return Usage{FiveHour: window("5h"), SevenDay: window("7d")}
}
