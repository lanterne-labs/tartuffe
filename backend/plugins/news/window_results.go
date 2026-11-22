package news

import (
	"time"

	"digital-mirror/backend/search"
)

// minDatedOutside is how many dated results, all outside the window, show
// that a search did not apply the date range.
const minDatedOutside = 3

// resultsInWindow keeps the search results the window [from, to] can hold:
// the undated ones, and the dated ones inside the window or within
// SearchWindowSlack of it. ignored reports a search that did not apply the
// range: minDatedOutside dated results or more, and none in the window.
//
// A provider that ignores the range answers a window of 2018 with the news of
// today. On 2026-11-21 the first provider ran out of credit and the next one
// answered 3 710 windows of 2016 to 2019 with up to 200 current articles
// each: the windows were logged as searched, so the worker would never have
// searched them again.
func resultsInWindow(results []search.NewsResult, from, to *time.Time) (kept []search.NewsResult, ignored bool) {
	if from == nil || to == nil || from.IsZero() || to.IsZero() {
		return results, false
	}
	lo, hi := from.Add(-SearchWindowSlack), to.Add(SearchWindowSlack)
	dated, inside := 0, 0
	for _, r := range results {
		if r.Date == nil {
			kept = append(kept, r)
			continue
		}
		dated++
		if r.Date.Before(lo) || r.Date.After(hi) {
			continue
		}
		inside++
		kept = append(kept, r)
	}
	return kept, inside == 0 && dated >= minDatedOutside
}
