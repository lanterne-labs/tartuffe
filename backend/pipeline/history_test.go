package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins"
)

// historyPlugin is a fakePlugin that reads history: each read is recorded,
// and a read that ends without an outage marks its stretch fetched (unless
// failFetch: its fetch failed). done holds the stretches fetched, keyed
// "since..until".
type historyPlugin struct {
	fakePlugin
	windows   *[]string
	done      map[string]bool
	failFetch bool
}

func stretch(since, until time.Time) string {
	return since.Format("2006-01-02") + ".." + until.Format("2006-01-02")
}

func (h historyPlugin) Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	*h.windows = append(*h.windows, fmt.Sprintf("%s %s/%d", h.name, stretch(*opts.Since, *opts.Until), opts.WindowDays))
	stats, err := h.fakePlugin.Ingest(ctx, d, targets, opts)
	if !h.failFetch && !errors.Is(err, errDown) {
		h.done[stretch(*opts.Since, *opts.Until)] = true
	}
	return stats, err
}

func (h historyPlugin) HistoryFetched(_ context.Context, _ *deps.Deps, _ []plugins.Target, since, until time.Time) (bool, error) {
	return h.done[stretch(since, until)], nil
}

type memCursors map[string]time.Time

func (m memCursors) GetHistoryCursor(_ context.Context, plugin string) (time.Time, bool, error) {
	t, ok := m[plugin]
	return t, ok, nil
}

func (m memCursors) SetHistoryCursor(_ context.Context, plugin string, reached time.Time) error {
	m[plugin] = reached
	return nil
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestHistoryWindow(t *testing.T) {
	now := day("2026-10-01")
	cases := []struct {
		name         string
		reached      time.Time
		read         bool
		since, until string
		ok           bool
	}{
		{"never read: the month before now", time.Time{}, false, "2026-09-01", "2026-10-01", true},
		{"the month before the cursor", day("2026-06-15"), true, "2026-05-16", "2026-06-15", true},
		{"floored at the depth", day("2023-10-20"), true, "2023-10-01", "2023-10-20", true},
		{"full depth reached", day("2023-10-01"), true, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			since, until, ok := historyWindow(tc.reached, tc.read, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got := since.Format("2006-01-02") + ".." + until.Format("2006-01-02"); got != tc.since+".."+tc.until {
				t.Errorf("window = %s, want %s..%s", got, tc.since, tc.until)
			}
		})
	}
}

// TestRunHistory: the cursor follows the plugin's fetch log: it moves over
// stretches already fetched, reads the next ones while the gate lets it, and
// stays on a stretch the read did not fetch.
func TestRunHistory(t *testing.T) {
	now := day("2026-10-01").Add(13 * time.Hour) // aligned to the day by runHistory
	targets := []plugins.Target{{PersonID: 1}, {PersonID: 2}}
	// gate lets n reads start, then reports the phase not started.
	gate := func(n int) func(string, func() error) error {
		return func(_ string, fn func() error) error {
			if n == 0 {
				return nil
			}
			n--
			return fn()
		}
	}

	t.Run("reads month after month while the gate is open", func(t *testing.T) {
		var calls, windows []string
		news := historyPlugin{fakePlugin: fakePlugin{name: "news", calls: &calls}, windows: &windows, done: map[string]bool{}}
		withPlugins(t, news, fakePlugin{name: "votes", calls: &calls})
		cursors := memCursors{}
		errs := runHistory(context.Background(), nil, cursors, targets, now, gate(2))
		if fmt.Sprint(windows) != "[news 2026-09-01..2026-10-01/3 news 2026-08-02..2026-09-01/3]" {
			t.Errorf("windows = %v", windows)
		}
		if fmt.Sprint(calls) != "[ingest:news:1:2 ingest:news:1:2]" {
			t.Errorf("calls = %v; votes reads no history", calls)
		}
		if !cursors["news"].Equal(day("2026-08-02")) || len(errs) != 0 {
			t.Errorf("cursor = %v errs = %v; want two months read", cursors["news"], errs)
		}
	})

	t.Run("stretches already fetched are not read again", func(t *testing.T) {
		var calls, windows []string
		done := map[string]bool{"2026-07-02..2026-08-01": true, "2026-06-02..2026-07-02": true}
		withPlugins(t, historyPlugin{fakePlugin: fakePlugin{name: "news", calls: &calls}, windows: &windows, done: done})
		cursors := memCursors{"news": day("2026-08-01")}
		runHistory(context.Background(), nil, cursors, targets, now, gate(1))
		if fmt.Sprint(windows) != "[news 2026-05-03..2026-06-02/3]" || !cursors["news"].Equal(day("2026-05-03")) {
			t.Errorf("windows = %v cursor = %v; want the two fetched months skipped, the third read", windows, cursors["news"])
		}
	})

	t.Run("an unfetched stretch keeps the cursor", func(t *testing.T) {
		var calls, windows []string
		withPlugins(t, historyPlugin{fakePlugin: fakePlugin{name: "news", err: errors.New("search quota exhausted"), calls: &calls},
			windows: &windows, done: map[string]bool{}, failFetch: true})
		cursors := memCursors{"news": day("2026-08-01")}
		errs := runHistory(context.Background(), nil, cursors, targets, now, gate(5))
		if !cursors["news"].Equal(day("2026-08-01")) || errs["history-news"] == nil || len(windows) != 1 {
			t.Errorf("cursor = %v errs = %v windows = %v; want one failed read, cursor kept", cursors["news"], errs, windows)
		}
	})

	t.Run("a fetched stretch with an extraction error moves on", func(t *testing.T) {
		var calls, windows []string
		withPlugins(t, historyPlugin{fakePlugin: fakePlugin{name: "news", err: errors.New("extraction failed"), calls: &calls},
			windows: &windows, done: map[string]bool{}})
		cursors := memCursors{"news": day("2026-08-01")}
		errs := runHistory(context.Background(), nil, cursors, targets, now, gate(1))
		if !cursors["news"].Equal(day("2026-07-02")) || errs["history-news"] == nil {
			t.Errorf("cursor = %v errs = %v; want the stretch done and the error reported", cursors["news"], errs)
		}
	})

	t.Run("an outage keeps the cursor", func(t *testing.T) {
		var calls, windows []string
		withPlugins(t, historyPlugin{fakePlugin: fakePlugin{name: "news", outageAt: 2, tripped: new(bool), calls: &calls},
			windows: &windows, done: map[string]bool{}})
		cursors := memCursors{"news": day("2026-08-01")}
		errs := runHistory(context.Background(), nil, cursors, targets, now, gate(1))
		if !cursors["news"].Equal(day("2026-08-01")) || errs["history-news"] == nil {
			t.Errorf("cursor = %v errs = %v; want the cursor kept and the outage reported", cursors["news"], errs)
		}
	})

	t.Run("no targets reads nothing", func(t *testing.T) {
		var calls, windows []string
		withPlugins(t, historyPlugin{fakePlugin: fakePlugin{name: "news", calls: &calls}, windows: &windows, done: map[string]bool{}})
		cursors := memCursors{}
		runHistory(context.Background(), nil, cursors, nil, now, gate(1))
		if len(calls) != 0 || len(cursors) != 0 {
			t.Errorf("calls = %v cursors = %v", calls, cursors)
		}
	})
}

// TestHistoryReaders: the press sources read history; official records and
// photos have their own scope.
func TestHistoryReaders(t *testing.T) {
	var got []string
	for _, p := range Plugins {
		if _, ok := p.(plugins.HistoryReader); ok {
			got = append(got, p.Name())
		}
	}
	if fmt.Sprint(got) != "[news twitter]" {
		t.Errorf("history readers = %v, want [news twitter]", got)
	}
}
