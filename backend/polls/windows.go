// The windows of the anonymous ballots. A ballot counts for one 30-day
// window; two series of windows run, the second shifted by 15 days, and the
// browser casts in both. The results read the series whose window started
// first, so they never start again from zero. A voter's nullifier differs in
// each window: a re-registration (a lost voting key) takes effect at the next
// window of each series, so one document never holds two commitments, and
// never casts two ballots, in one window.
package polls

import (
	"fmt"
	"time"

	"digital-mirror/backend/polls/semaphore"
)

// WindowLength is how long a ballot counts.
const WindowLength = 30 * 24 * time.Hour

// seriesShift is how far the second series is from the first.
const seriesShift = WindowLength / 2

// Series counts the series of windows (0 and 1).
const Series = 2

// windowEpoch is where window 0 of series 0 starts.
var windowEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Window is one window of one series.
type Window struct {
	Series int `json:"series"`
	Index  int `json:"window"`
}

// WindowAt is the window of the series that holds t.
func WindowAt(series int, t time.Time) Window {
	d := t.Sub(windowEpoch.Add(time.Duration(series) * seriesShift))
	i := int(d / WindowLength)
	if d < 0 && d%WindowLength != 0 {
		i--
	}
	return Window{Series: series, Index: i}
}

// Start is when the window opens.
func (w Window) Start() time.Time {
	return windowEpoch.Add(time.Duration(w.Series)*seriesShift + time.Duration(w.Index)*WindowLength)
}

// End is when the window closes (the next one opens).
func (w Window) End() time.Time { return w.Start().Add(WindowLength) }

// OpenWindows are the windows of both series that hold t.
func OpenWindows(t time.Time) [Series]Window {
	var out [Series]Window
	for s := range Series {
		out[s] = WindowAt(s, t)
	}
	return out
}

// ResultWindow is the open window the results read: the one that started
// first, so it holds the most ballots.
func ResultWindow(t time.Time) Window {
	open := OpenWindows(t)
	best := open[0]
	for _, w := range open[1:] {
		if w.Start().Before(best.Start()) {
			best = w
		}
	}
	return best
}

// ScopeName names the scope of the event's ballots in the window: the
// nullifier of a voter is the same for each of their ballots under it.
func ScopeName(eventID int64, w Window) string {
	return fmt.Sprintf("tartuffe/ballot/v1/%d/%d/%d", eventID, w.Series, w.Index)
}

// Scope is the scope value the browser proves under.
func Scope(eventID int64, w Window) string {
	return semaphore.Digest([]byte(ScopeName(eventID, w))).String()
}
