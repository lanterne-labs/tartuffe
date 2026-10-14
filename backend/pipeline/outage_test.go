package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
)

var errDown = fmt.Errorf("%w: primary down; fallback failed", llm.ErrUnavailable)

// fakePlugin records each call, with the resume options it got. Ingest walks
// the targets in order and stops at outageAt once (tripped), returning
// errDown joined with preErr (a failure met before the outage) and reporting
// the targets done before it, the interrupted one as fetched, and one spent
// unit per done target; err is returned by every other call. Backfill returns
// the next of backfillErrs (nil once they are used up).
type fakePlugin struct {
	name         string
	err          error
	preErr       error
	outageAt     int64
	tripped      *bool
	backfillErrs *[]error
	calls        *[]string
}

func (f fakePlugin) Name() string { return f.name }

func (f fakePlugin) Ingest(_ context.Context, _ *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	call := "ingest:" + f.name
	for _, t := range targets {
		call += fmt.Sprint(":", t.PersonID)
	}
	if len(opts.ExtractOnly) > 0 || opts.Spent > 0 {
		call += fmt.Sprintf(" extract=%v spent=%d", opts.ExtractOnly, opts.Spent)
	}
	*f.calls = append(*f.calls, call)
	var stats plugins.IngestStats
	for _, t := range targets {
		if t.PersonID == f.outageAt && !*f.tripped {
			*f.tripped = true
			stats.Fetched = []int64{t.PersonID}
			stats.Spent = len(stats.Done)
			return stats, errors.Join(f.preErr, errDown)
		}
		stats.Done = append(stats.Done, t.PersonID)
		stats.Targets++
	}
	stats.Spent = len(stats.Done)
	return stats, f.err
}

func (f fakePlugin) Backfill(context.Context, *deps.Deps, time.Time) error {
	*f.calls = append(*f.calls, "backfill:"+f.name)
	if f.backfillErrs == nil || len(*f.backfillErrs) == 0 {
		return nil
	}
	err := (*f.backfillErrs)[0]
	*f.backfillErrs = (*f.backfillErrs)[1:]
	return err
}

func withPlugins(t *testing.T, ps ...plugins.Plugin) {
	t.Helper()
	saved := Plugins
	Plugins = ps
	t.Cleanup(func() { Plugins = saved })
}

// fakeWait is a runGate.waitLLM that reports back (after polls probes) and
// counts its calls.
func fakeWait(back bool, polls int, calls *int) func(context.Context, error, time.Time) (bool, int) {
	return func(context.Context, error, time.Time) (bool, int) {
		*calls++
		return back, polls
	}
}

// TestGateWaitsAndRetries: a phase that hits an LLM outage is run again once
// the LLM is back; the run goes on, is not failed, and notes the outage. Other
// failures are returned without waiting.
func TestGateWaitsAndRetries(t *testing.T) {
	var waits int
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: fakeWait(true, 3, &waits)}
	tries := 0
	err := g.run(context.Background(), "stance-fanout", func() error {
		tries++
		if tries == 1 {
			return fmt.Errorf("routing fact 7: %w", errDown)
		}
		return nil
	})
	if err != nil || tries != 2 || waits != 1 {
		t.Fatalf("err=%v tries=%d waits=%d; want the phase retried once after one wait", err, tries, waits)
	}
	if !g.open("translate") || g.err() != nil {
		t.Fatal("a recovered outage must not stop or fail the run")
	}
	if g.outages != 1 || g.note() == "" {
		t.Errorf("outages=%d note=%q; want the outage noted", g.outages, g.note())
	}

	if err := g.run(context.Background(), "merge", func() error { return errors.New("bad row") }); err == nil || waits != 1 {
		t.Errorf("err=%v waits=%d: a non-LLM failure is returned without waiting", err, waits)
	}
}

// TestGateStopsWhenLLMNotBack: when the LLM is not back by the deadline the
// run stops at that phase: no later LLM phase starts and the run error names
// the phase. A gate that cannot wait (smoke run) stops at once.
func TestGateStopsWhenLLMNotBack(t *testing.T) {
	var waits int
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: fakeWait(false, 3, &waits)}
	err := g.run(context.Background(), "stance-fanout", func() error { return errDown })
	if !errors.Is(err, llm.ErrUnavailable) || waits != 1 {
		t.Fatalf("err=%v waits=%d; want the outage returned after one wait", err, waits)
	}
	if g.open("contradictions") || g.open("translate") {
		t.Fatal("no LLM phase may start after the run stopped")
	}
	if want := "LLM unavailable: stopped after phase stance-fanout"; g.err() == nil || g.err().Error() != want {
		t.Errorf("run error = %v, want %q", g.err(), want)
	}

	smoke := runGate{deadline: time.Now().Add(time.Hour)}
	tries := 0
	_ = smoke.run(context.Background(), "ingest", func() error { tries++; return errDown })
	if tries != 1 || smoke.err() == nil || smoke.outages != 0 {
		t.Errorf("tries=%d err=%v outages=%d; a smoke run stops without waiting", tries, smoke.err(), smoke.outages)
	}
}

// TestGateGivesUpQuickRefails: a phase that keeps failing as unavailable
// while the LLM answers on the first poll is given up after maxQuickRefails
// retries, as a phase error; the run is not stopped. A real outage in between
// (several polls) resets the count.
func TestGateGivesUpQuickRefails(t *testing.T) {
	var waits int
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: fakeWait(true, 1, &waits)}
	tries := 0
	err := g.run(context.Background(), "ingest", func() error { tries++; return errDown })
	if !errors.Is(err, errRefails) || !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v; want the phase given up", err)
	}
	if tries != maxQuickRefails+1 {
		t.Errorf("tries = %d, want %d", tries, maxQuickRefails+1)
	}
	if g.err() != nil || !g.open("translate") {
		t.Error("giving a phase up must not stop the run")
	}

	polls := []int{1, 1, 4, 1, 1}
	g = runGate{deadline: time.Now().Add(time.Hour), waitLLM: func(context.Context, error, time.Time) (bool, int) {
		p := polls[0]
		polls = polls[1:]
		return true, p
	}}
	tries = 0
	err = g.run(context.Background(), "ingest", func() error {
		if tries++; tries <= 5 {
			return errDown
		}
		return nil
	})
	if err != nil || tries != 6 {
		t.Errorf("err=%v tries=%d; a real outage resets the quick-refail count", err, tries)
	}
}

// TestGateNoResumeAfterDeadline: an LLM back only after the run deadline does
// not resume the phase; the run stops there.
func TestGateNoResumeAfterDeadline(t *testing.T) {
	g := runGate{deadline: time.Now().Add(20 * time.Millisecond), waitLLM: func(context.Context, error, time.Time) (bool, int) {
		time.Sleep(40 * time.Millisecond)
		return true, 3
	}}
	tries := 0
	err := g.run(context.Background(), "translate", func() error { tries++; return errDown })
	if !errors.Is(err, llm.ErrUnavailable) || tries != 1 || g.err() == nil {
		t.Errorf("err=%v tries=%d runErr=%v; want no retry past the deadline and the run stopped", err, tries, g.err())
	}
}

// TestMergeAuditTries: a retried audit's counts stand, plus the rows the
// earlier try deleted (they are not listed again).
func TestMergeAuditTries(t *testing.T) {
	first := &contradiction.AuditRunResult{Total: 10, Keep: 3, Remove: 2, Deleted: 1, Unstable: 1}
	second := &contradiction.AuditRunResult{Total: 9, Keep: 8, Cached: 3, Remove: 1, Deleted: 1}
	got := mergeAuditTries(mergeAuditTries(nil, first), second)
	if got.Total != 10 || got.Deleted != 2 || got.Remove != 2 || got.Keep != 8 {
		t.Errorf("merged = %+v; want Total 10, Deleted 2, Remove 2, Keep 8", got)
	}
	if mergeAuditTries(first, nil) != first {
		t.Error("a try that did not audit keeps the earlier result")
	}
}

// TestBudgetedRetryGetsWhatIsLeft: a retried phase gets the budget the
// earlier tries left, and none starts once it is spent.
func TestBudgetedRetryGetsWhatIsLeft(t *testing.T) {
	var waits int
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: fakeWait(true, 3, &waits)}
	var got []int
	used := []int{300, 200, 500}
	err := g.run(context.Background(), "translate", budgeted(750, func(left int) (int, error) {
		got = append(got, left)
		n := used[len(got)-1]
		if n >= left {
			return left, nil
		}
		return n, errDown
	}))
	if err != nil || fmt.Sprint(got) != "[750 450 250]" {
		t.Fatalf("err=%v budgets=%v; want [750 450 250]", err, got)
	}

	spent := budgeted(10, func(int) (int, error) { return 10, errDown })
	_ = spent()
	if err := spent(); err != nil {
		t.Fatalf("a spent budget must not start the phase: %v", err)
	}
}

// TestRunGateDeadline: after the run deadline no phase starts, and the run
// is not recorded as an outage.
func TestRunGateDeadline(t *testing.T) {
	g := runGate{deadline: time.Now().Add(-time.Minute)}
	if g.open("translate") {
		t.Fatal("a phase started after the run deadline")
	}
	if g.err() != nil {
		t.Fatalf("a reached deadline is not an error: %v", g.err())
	}
	g.deadline = time.Now().Add(time.Hour)
	if !g.open("translate") {
		t.Fatal("a phase before the deadline must start")
	}
}

// TestRunDeadline: the deadline is the next 23:00 Paris after the start; a
// run started after 23:00 gets the next day's.
func TestRunDeadline(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ start, want time.Time }{
		{time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC), time.Date(2026, 9, 24, 23, 0, 0, 0, paris)},
		{time.Date(2026, 9, 24, 21, 30, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, paris)},
	}
	for _, c := range cases {
		got, err := runDeadline(c.start)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("runDeadline(%v) = %v, %v; want %v", c.start, got, err, c.want)
		}
	}
}

// TestIngestResumesAfterOutage: an outage stops the phase at that plugin (the
// later plugins do not start; another plugin's failure does not stop it).
// The retry runs neither the finished plugins nor the targets the interrupted
// plugin finished, only extracts the one it had fetched, passes the cap it
// spent, and keeps the interrupted call's other failures but not its outage.
func TestIngestResumesAfterOutage(t *testing.T) {
	var calls []string
	withPlugins(t,
		fakePlugin{name: "a", err: errors.New("serp quota"), calls: &calls},
		fakePlugin{name: "b", outageAt: 2, preErr: errors.New("fetch quota"), tripped: new(bool), calls: &calls},
		fakePlugin{name: "c", calls: &calls},
	)
	targets := []plugins.Target{{PersonID: 1}, {PersonID: 2}, {PersonID: 3}}
	var in IngestRun
	err := in.Run(context.Background(), nil, targets, plugins.IngestOptions{}, nil)
	if !errors.Is(err, llm.ErrUnavailable) || !errors.Is(in.Errs["b"], llm.ErrUnavailable) {
		t.Fatalf("err=%v errs=%v; want b's outage", err, in.Errs)
	}
	if fmt.Sprint(calls) != "[ingest:a:1:2:3 ingest:b:1:2:3]" {
		t.Fatalf("calls = %v; c must not start after b's outage", calls)
	}

	calls = nil
	if err := in.Run(context.Background(), nil, targets, plugins.IngestOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[ingest:b:2:3 extract=[2] spent=1 ingest:c:1:2:3]" {
		t.Errorf("calls = %v; want b resumed at target 2 (extraction only) with 1 spent, then c", calls)
	}
	if bErr := in.Errs["b"]; bErr == nil || errors.Is(bErr, llm.ErrUnavailable) || bErr.Error() != "fetch quota" {
		t.Errorf("b's error = %v; want its earlier failure kept, its outage dropped", bErr)
	}
	if in.Errs["a"] == nil || in.Errs["c"] != nil {
		t.Errorf("errs = %v; want a's failure kept", in.Errs)
	}
	if b := in.Result.ByPlugin["b"]; b.Targets != 3 || fmt.Sprint(b.Done) != "[1 2 3]" || b.Spent != 3 {
		t.Errorf("b stats = %+v; want every target counted once", b)
	}

	calls = nil
	if err := in.Run(context.Background(), nil, targets, plugins.IngestOptions{}, nil); err != nil || len(calls) != 0 {
		t.Errorf("err=%v calls=%v; a finished phase runs nothing", err, calls)
	}
}

// TestIngestAbandon: a plugin given up after an outage keeps its error and the
// next Run goes on with the later plugins.
func TestIngestAbandon(t *testing.T) {
	var calls []string
	withPlugins(t,
		fakePlugin{name: "a", outageAt: 1, tripped: new(bool), calls: &calls},
		fakePlugin{name: "b", calls: &calls},
	)
	targets := []plugins.Target{{PersonID: 1}}
	var in IngestRun
	if err := in.Run(context.Background(), nil, targets, plugins.IngestOptions{}, nil); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v, want a's outage", err)
	}
	if !in.Abandon() || in.Abandon() {
		t.Fatal("Abandon gives up the interrupted plugin once")
	}
	calls = nil
	if err := in.Run(context.Background(), nil, targets, plugins.IngestOptions{}, nil); err != nil || fmt.Sprint(calls) != "[ingest:b:1]" {
		t.Fatalf("err=%v calls=%v; want b only", err, calls)
	}
	if !errors.Is(in.Errs["a"], llm.ErrUnavailable) {
		t.Errorf("errs = %v; the abandoned plugin keeps its outage error", in.Errs)
	}
}

// TestBackfillResumesAfterOutage: the same rule holds for backfills, and a
// plugin reached after the deadline does not start.
func TestBackfillResumesAfterOutage(t *testing.T) {
	var calls []string
	aErrs := []error{errDown}
	withPlugins(t,
		fakePlugin{name: "a", backfillErrs: &aErrs, calls: &calls},
		fakePlugin{name: "b", calls: &calls},
	)
	var bf BackfillRun
	deadline := time.Now().Add(time.Hour)
	if err := bf.Run(context.Background(), nil, deadline); !errors.Is(err, llm.ErrUnavailable) || fmt.Sprint(calls) != "[backfill:a]" {
		t.Fatalf("err=%v calls=%v; b must not start after a's outage", err, calls)
	}
	calls = nil
	if err := bf.Run(context.Background(), nil, deadline); err != nil || fmt.Sprint(calls) != "[backfill:a backfill:b]" || len(bf.Errs) != 0 {
		t.Fatalf("err=%v calls=%v errs=%v; want a retried, then b", err, calls, bf.Errs)
	}
	calls = nil
	if err := bf.Run(context.Background(), nil, deadline); err != nil || len(calls) != 0 {
		t.Errorf("err=%v calls=%v; a finished phase runs nothing", err, calls)
	}

	var late BackfillRun
	if err := late.Run(context.Background(), nil, time.Now().Add(-time.Minute)); err != nil || len(calls) != 0 {
		t.Errorf("err=%v calls=%v; no backfill starts after the deadline", err, calls)
	}
}

// TestPhasesStopAtDeadline: once the context is done no plugin of the ingest
// or backfill phase starts, and the returned error is a deadline cut.
func TestPhasesStopAtDeadline(t *testing.T) {
	var calls []string
	withPlugins(t, fakePlugin{name: "a", calls: &calls}, fakePlugin{name: "b", calls: &calls})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	var in IngestRun
	err := in.Run(ctx, nil, []plugins.Target{{PersonID: 1}}, plugins.IngestOptions{}, nil)
	if cut, rest := splitCut(ctx, err); !cut || rest != nil || len(calls) != 0 {
		t.Fatalf("err=%v calls=%v; want no ingest started and a deadline cut", err, calls)
	}

	var bf BackfillRun
	err = bf.Run(ctx, nil, time.Now().Add(time.Hour))
	cut, rest := splitCut(ctx, err)
	errCut, errRest := splitCut(ctx, bf.Errs["a"])
	if !cut || rest != nil || len(calls) != 0 || !errCut || errRest != nil {
		t.Fatalf("err=%v calls=%v errs=%v; want no backfill started and a deadline cut", err, calls, bf.Errs)
	}
}

// TestSplitCut: only the parts that are the step's own context expiring are a
// cut; a client's own timeout, or any other failure joined with the cut, stays
// a step error.
func TestSplitCut(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	live := context.Background()
	stopped := fmt.Errorf("frpolitics: votes: %w", fmt.Errorf("law 17-536 stopped: %w", expired.Err()))
	clientTimeout := fmt.Errorf("downloading archive: %w", context.DeadlineExceeded)
	scrape := errors.New("scrape failed")

	if cut, rest := splitCut(expired, stopped); !cut || rest != nil {
		t.Errorf("wrapped expiry: cut=%v rest=%v; want a pure cut", cut, rest)
	}
	if cut, rest := splitCut(live, clientTimeout); cut || rest == nil {
		t.Errorf("client timeout with the step context live: cut=%v rest=%v; want a step error", cut, rest)
	}
	if cut, rest := splitCut(expired, errDown); cut || !errors.Is(rest, llm.ErrUnavailable) {
		t.Errorf("outage: cut=%v rest=%v; want the outage kept", cut, rest)
	}
	joined := fmt.Errorf("ingest twitter: %w", errors.Join(scrape, stopped))
	cut, rest := splitCut(expired, joined)
	if !cut || rest == nil || !errors.Is(rest, scrape) || errors.Is(rest, context.DeadlineExceeded) {
		t.Fatalf("joined: cut=%v rest=%v; want the cut split off and the scrape failure kept", cut, rest)
	}
	if rest.Error() != "ingest twitter: scrape failed" {
		t.Errorf("rest = %q; want the wrapper's context kept", rest.Error())
	}
}

// TestGateWaitCutByPhaseBudget: a phase whose own budget runs out while it
// waits for the LLM is cut; the run is not stopped and later phases start.
func TestGateWaitCutByPhaseBudget(t *testing.T) {
	waits := 0
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: fakeWait(false, 3, &waits)}
	phaseCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := g.run(phaseCtx, "ingest", func() error { return errDown })
	if cut, rest := splitCut(phaseCtx, err); !cut || rest != nil {
		t.Fatalf("err = %v; want a cut of the phase", err)
	}
	if g.err() != nil || !g.open("translate") {
		t.Errorf("gate stopped (%v); a phase budget is not an outage of the run", g.err())
	}
}

// TestNotificationNotes: the LLM wait and the deadline cuts are notes of the
// message, not errors.
func TestNotificationNotes(t *testing.T) {
	msg := notification("success", false, time.Hour, nil,
		[]string{"LLM outages waited for: 1 (5m0s)", "stopped at the deadline (resumed next run): ingest"},
		plugins.IngestStats{}, nil)
	for _, want := range []string{"✅", "⏸ LLM outages waited for", "⏸ stopped at the deadline (resumed next run): ingest"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notification lacks %q:\n%s", want, msg)
		}
	}
}

// TestGateWaitBudget: the waits of one run share maxLLMWait. A wait ends at
// the end of the budget when it comes before the deadline, and a run whose
// budget is spent stops at the next outage without waiting.
func TestGateWaitBudget(t *testing.T) {
	var until time.Time
	g := runGate{deadline: time.Now().Add(10 * time.Hour), waitLLM: func(_ context.Context, _ error, u time.Time) (bool, int) {
		until = u
		return false, 3
	}}
	start := time.Now()
	_ = g.run(context.Background(), "ingest", func() error { return errDown })
	if until.Before(start.Add(maxLLMWait)) || until.After(time.Now().Add(maxLLMWait)) {
		t.Fatalf("wait until %v; want the end of the %v budget, not the deadline %v", until, maxLLMWait, g.deadline)
	}

	var waits int
	spent := runGate{deadline: time.Now().Add(10 * time.Hour), waitLLM: fakeWait(true, 3, &waits), waited: maxLLMWait}
	err := spent.run(context.Background(), "ingest", func() error { return errDown })
	if !errors.Is(err, llm.ErrUnavailable) || waits != 0 || spent.open("translate") {
		t.Fatalf("err=%v waits=%d; a run with a spent budget must stop without waiting", err, waits)
	}
}

// seatLimit is the error of a Claude seat at its usage limit until retryAt.
func seatLimit(retryAt time.Time) error {
	return &llm.UnavailableError{
		Endpoint: llm.EndpointClaudeTeam,
		RetryAt:  retryAt,
		Err:      fmt.Errorf("%w: claude seat %q reached its usage limit", llm.ErrUnavailable, "alice"),
	}
}

// gpuProbes points OPEN_AI_API at a healthy server and counts its requests.
func gpuProbes(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL)
	return &hits
}

// TestGateWaitsForSeatReset: a phase stopped by a Claude seat limit runs
// again at the reset time of the seat. The gate does not probe the local GPU.
func TestGateWaitsForSeatReset(t *testing.T) {
	probes := gpuProbes(t)
	g := runGate{deadline: time.Now().Add(time.Hour), waitLLM: llm.WaitForEndpoint}
	retryAt := time.Now().Add(100 * time.Millisecond)
	var tries int
	var retried time.Time
	err := g.run(context.Background(), "contradictions", func() error {
		if tries++; tries == 1 {
			return fmt.Errorf("detect pair 3: %w", seatLimit(retryAt))
		}
		retried = time.Now()
		return nil
	})
	if err != nil || tries != 2 || retried.Before(retryAt) {
		t.Fatalf("err=%v tries=%d retried=%v; want 1 retry at or after %v", err, tries, retried, retryAt)
	}
	if probes.Load() != 0 || g.outages != 1 || g.err() != nil {
		t.Fatalf("probes=%d outages=%d run error=%v; want no GPU probe and 1 outage", probes.Load(), g.outages, g.err())
	}
}

// TestGateStopsAtLateSeatReset: a seat that resets after the end of the wait
// budget, or at a time that is not known, stops the run at the phase at once;
// the next run resumes it.
func TestGateStopsAtLateSeatReset(t *testing.T) {
	probes := gpuProbes(t)
	g := runGate{deadline: time.Now().Add(10 * time.Hour), waitLLM: llm.WaitForEndpoint}
	start := time.Now()
	err := g.run(context.Background(), "contradictions", func() error { return seatLimit(time.Now().Add(3 * time.Hour)) })
	if !errors.Is(err, llm.ErrUnavailable) || g.err() == nil || g.open("translate") {
		t.Fatalf("err=%v run error=%v; want the run stopped at the phase", err, g.err())
	}
	if time.Since(start) > time.Second || probes.Load() != 0 {
		t.Fatalf("stopped after %v with %d GPU probes; want at once and no probe", time.Since(start), probes.Load())
	}

	// A reset text that does not parse gives no RetryAt: the run stops too.
	g = runGate{deadline: time.Now().Add(10 * time.Hour), waitLLM: llm.WaitForEndpoint}
	err = g.run(context.Background(), "contradictions", func() error { return seatLimit(time.Time{}) })
	if !errors.Is(err, llm.ErrUnavailable) || g.err() == nil || probes.Load() != 0 {
		t.Fatalf("err=%v run error=%v probes=%d; an unknown reset must stop the run without a probe", err, g.err(), probes.Load())
	}
}
