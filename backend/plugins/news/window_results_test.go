package news

import (
	"testing"
	"time"

	"digital-mirror/backend/search"
)

func TestResultsInWindow(t *testing.T) {
	day := func(s string) *time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	from, to := day("2018-04-14"), day("2018-04-17")
	res := func(dates ...string) []search.NewsResult {
		var out []search.NewsResult
		for _, d := range dates {
			r := search.NewsResult{Link: "https://example.org/" + d}
			if d != "" {
				r.Date = day(d)
			}
			out = append(out, r)
		}
		return out
	}

	// The provider ignored the range: the news of 2026 for a window of 2018.
	kept, ignored := resultsInWindow(res("2026-11-18", "2026-11-20", "2026-11-09"), from, to)
	if !ignored || len(kept) != 0 {
		t.Errorf("current news for an old window: kept %d, ignored %v; want 0, true", len(kept), ignored)
	}
	// One stray result among results of the window is dropped alone.
	kept, ignored = resultsInWindow(res("2018-04-15", "2026-11-20", "2018-04-16", ""), from, to)
	if ignored || len(kept) != 3 {
		t.Errorf("one stray result: kept %d, ignored %v; want 3, false", len(kept), ignored)
	}
	// Two results outside and none inside do not condemn the search.
	kept, ignored = resultsInWindow(res("2026-11-18", "2026-11-20"), from, to)
	if ignored || len(kept) != 0 {
		t.Errorf("two results outside: kept %d, ignored %v; want 0, false", len(kept), ignored)
	}
	// Undated results stay; a search without a window keeps everything.
	if kept, ignored = resultsInWindow(res("", ""), from, to); ignored || len(kept) != 2 {
		t.Errorf("undated results: kept %d, ignored %v; want 2, false", len(kept), ignored)
	}
	if kept, ignored = resultsInWindow(res("2026-11-18", "2026-11-19", "2026-11-20"), nil, nil); ignored || len(kept) != 3 {
		t.Errorf("no window: kept %d, ignored %v; want 3, false", len(kept), ignored)
	}
}
