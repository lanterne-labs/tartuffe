package core

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"digital-mirror/backend/llm"
)

// memDocs is an in-memory DocumentStorer: documents keyed by id, states
// recorded exactly as the engine writes them.
type memDocs struct {
	docs   []SourceDocument
	states map[int64]string
	facts  map[int64]int
	tries  map[int64]int
}

func newMemDocs(docs ...SourceDocument) *memDocs {
	m := &memDocs{docs: docs, states: map[int64]string{}, facts: map[int64]int{}, tries: map[int64]int{}}
	for _, d := range docs {
		m.states[d.ID] = d.State
	}
	return m
}

func (m *memDocs) SaveFetchedDocuments(ctx context.Context, docs []SourceDocument) (map[string]int64, error) {
	return nil, errors.New("not used")
}
func (m *memDocs) KnownDocumentURLs(ctx context.Context, sourceType string, urls []string) (map[string]bool, error) {
	return nil, errors.New("not used")
}
func (m *memDocs) ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, publishedSince time.Time, limit int) ([]SourceDocument, error) {
	var out []SourceDocument
	for _, d := range m.docs {
		if !publishedSince.IsZero() && (d.PublishedAt == nil || d.PublishedAt.Before(publishedSince)) {
			continue
		}
		st := m.states[d.ID]
		if st == DocFetched || (st == DocFailed && m.tries[d.ID] < MaxExtractionAttempts) {
			out = append(out, d)
		}
	}
	return out, nil
}
func (m *memDocs) MarkDocumentExtracted(ctx context.Context, id int64, factsCount int) error {
	m.states[id] = DocExtracted
	m.facts[id] = factsCount
	m.tries[id]++
	return nil
}
func (m *memDocs) MarkDocumentFailed(ctx context.Context, id int64, extractErr error) error {
	m.states[id] = DocFailed
	m.tries[id]++
	return nil
}

func day(d int) *time.Time {
	t := time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func fetched(id int64, url string, d int) SourceDocument {
	return SourceDocument{ID: id, SourceType: "t", URL: url, State: DocFetched, PublishedAt: day(d)}
}

func newestFirst(a, b SourceDocument) bool { return a.PublishedAt.After(*b.PublishedAt) }

// The scheduling the engine owns for every plugin: order hook, MaxDocs,
// a failed document stays unfinished, a deadline already passed attempts
// nothing, an outage aborts on the failure streak, and a run where every
// attempt failed is an error.
func TestProcessDocuments_Scheduling(t *testing.T) {
	docs := func() []SourceDocument {
		return []SourceDocument{fetched(1, "A", 1), fetched(2, "B", 3), fetched(3, "C", 2), fetched(4, "D", 4)}
	}
	extractOne := func(seen *[]string, fail string) ExtractBatchFunc {
		return func(_ context.Context, batch []SourceDocument) (map[int64]int, error) {
			d := batch[0]
			*seen = append(*seen, d.URL)
			if d.URL == fail {
				return nil, errors.New("boom")
			}
			return map[int64]int{d.ID: 2}, nil
		}
	}

	// Newest first, MaxDocs=2: D then B; B fails and stays unfinished.
	m := newMemDocs(docs()...)
	var seen []string
	st, err := ProcessDocuments(context.Background(), m, "t", 0, extractOne(&seen, "B"), ProcessOptions{MaxDocs: 2, Order: newestFirst})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[D B]" || st.Attempted != 2 || st.Extracted != 1 || st.Failed != 1 || st.Facts != 2 || st.Pending != 4 {
		t.Fatalf("seen=%v stats=%+v", seen, st)
	}
	if m.states[4] != DocExtracted || m.states[2] != DocFailed || m.states[1] != DocFetched {
		t.Fatalf("states=%v", m.states)
	}

	// A deadline already passed: nothing attempted, flagged.
	m = newMemDocs(docs()...)
	seen = nil
	st, err = ProcessDocuments(context.Background(), m, "t", 0, extractOne(&seen, ""), ProcessOptions{Deadline: time.Now().Add(-time.Second)})
	if err != nil || len(seen) != 0 || !st.DeadlineHit {
		t.Fatalf("seen=%v stats=%+v err=%v", seen, st, err)
	}

	// No order hook: the storage order (oldest fetch first); all extracted.
	m = newMemDocs(docs()...)
	seen = nil
	st, err = ProcessDocuments(context.Background(), m, "t", 0, extractOne(&seen, ""), ProcessOptions{})
	if err != nil || fmt.Sprint(seen) != "[A B C D]" || st.Extracted != 4 {
		t.Fatalf("seen=%v stats=%+v err=%v", seen, st, err)
	}

	// An oldest-published-first hook: A, C, B, D.
	m = newMemDocs(docs()...)
	seen = nil
	oldestFirst := func(a, b SourceDocument) bool { return a.PublishedAt.Before(*b.PublishedAt) }
	_, err = ProcessDocuments(context.Background(), m, "t", 0, extractOne(&seen, ""), ProcessOptions{Order: oldestFirst})
	if err != nil || fmt.Sprint(seen) != "[A C B D]" {
		t.Fatalf("seen=%v err=%v", seen, err)
	}

	// Every document fails: an outage error, every document left failed.
	m = newMemDocs(docs()...)
	seen = nil
	_, err = ProcessDocuments(context.Background(), m, "t", 0, func(context.Context, []SourceDocument) (map[int64]int, error) {
		return nil, errors.New("outage")
	}, ProcessOptions{})
	if !errors.Is(err, ErrOutage) {
		t.Fatalf("want ErrOutage, got %v", err)
	}
	for id := int64(1); id <= 4; id++ {
		if m.states[id] != DocFailed {
			t.Fatalf("doc %d state %s, want failed", id, m.states[id])
		}
	}

	// A long backlog under an outage stops at the failure streak.
	var long []SourceDocument
	for i := 1; i <= 30; i++ {
		long = append(long, fetched(int64(i), fmt.Sprintf("L%02d", i), 1))
	}
	m = newMemDocs(long...)
	st, err = ProcessDocuments(context.Background(), m, "t", 0, func(context.Context, []SourceDocument) (map[int64]int, error) {
		return nil, errors.New("outage")
	}, ProcessOptions{MaxConsecutiveFailures: 10})
	if !errors.Is(err, ErrOutage) || st.Attempted != 10 {
		t.Fatalf("stats=%+v err=%v, want the abort after 10", st, err)
	}
}

// Batches: one extract call per BatchSize documents; a failed batch fails
// each of its documents; facts are credited per document; a document the
// extractor did not mention is extracted with zero facts.
func TestProcessDocuments_Batches(t *testing.T) {
	m := newMemDocs(fetched(1, "A", 1), fetched(2, "B", 1), fetched(3, "C", 1), fetched(4, "D", 1), fetched(5, "E", 1))
	var calls int
	st, err := ProcessDocuments(context.Background(), m, "t", 0, func(_ context.Context, batch []SourceDocument) (map[int64]int, error) {
		calls++
		if batch[0].URL == "C" {
			return nil, errors.New("boom")
		}
		return map[int64]int{batch[0].ID: 3}, nil
	}, ProcessOptions{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || st.Attempted != 5 || st.Extracted != 3 || st.Failed != 2 || st.Facts != 6 {
		t.Fatalf("calls=%d stats=%+v", calls, st)
	}
	if m.states[1] != DocExtracted || m.facts[1] != 3 || m.facts[2] != 0 || m.states[3] != DocFailed || m.states[4] != DocFailed || m.states[5] != DocExtracted {
		t.Fatalf("states=%v facts=%v", m.states, m.facts)
	}
}

// A failed document is retried on the next run until the attempt cap.
func TestProcessDocuments_RetriesUntilCap(t *testing.T) {
	m := newMemDocs(fetched(1, "A", 1))
	boom := func(context.Context, []SourceDocument) (map[int64]int, error) { return nil, errors.New("boom") }
	for i := 0; i < MaxExtractionAttempts; i++ {
		st, _ := ProcessDocuments(context.Background(), m, "t", 0, boom, ProcessOptions{})
		if st.Attempted != 1 {
			t.Fatalf("run %d: attempted %d, want 1", i+1, st.Attempted)
		}
	}
	st, err := ProcessDocuments(context.Background(), m, "t", 0, boom, ProcessOptions{})
	if err != nil || st.Pending != 0 {
		t.Fatalf("after %d attempts the document must be left alone: stats=%+v err=%v", MaxExtractionAttempts, st, err)
	}
}

// An unreachable LLM is not the documents' fault: the run still aborts as an
// outage, but no document is charged an attempt, so a night-long outage
// cannot use up their retries.
func TestProcessDocuments_UnavailableLLMKeepsAttempts(t *testing.T) {
	var long []SourceDocument
	for i := 1; i <= 30; i++ {
		long = append(long, fetched(int64(i), fmt.Sprintf("L%02d", i), 1))
	}
	m := newMemDocs(long...)
	down := func(context.Context, []SourceDocument) (map[int64]int, error) {
		return nil, fmt.Errorf("extracting: %w", fmt.Errorf("%w: primary failed; fallback failed: 402", llm.ErrUnavailable))
	}
	for run := 0; run < MaxExtractionAttempts+1; run++ {
		st, err := ProcessDocuments(context.Background(), m, "t", 0, down, ProcessOptions{MaxConsecutiveFailures: 10})
		if !errors.Is(err, ErrOutage) || !errors.Is(err, llm.ErrUnavailable) || st.Attempted != 10 || st.Pending != 30 {
			t.Fatalf("run %d: stats=%+v err=%v, want the outage abort after 10 with all 30 pending", run+1, st, err)
		}
	}
	for id := int64(1); id <= 30; id++ {
		if m.states[id] != DocFetched || m.tries[id] != 0 {
			t.Fatalf("doc %d: state %s tries %d, want fetched with no attempt charged", id, m.states[id], m.tries[id])
		}
	}
}

// A run cut by its deadline stops with the context's error: an expired
// context attempts nothing, and a batch cut under the cancellation is not
// charged to its documents, which stay fetched for the next run.
func TestProcessDocuments_StopsOnDeadline(t *testing.T) {
	m := newMemDocs(fetched(1, "A", 1), fetched(2, "B", 2))
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	calls := 0
	st, err := ProcessDocuments(expired, m, "t", 0, func(context.Context, []SourceDocument) (map[int64]int, error) {
		calls++
		return nil, nil
	}, ProcessOptions{})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 || st.Attempted != 0 {
		t.Fatalf("calls=%d stats=%+v err=%v; want no extract and a deadline error", calls, st, err)
	}

	ctx, cut := context.WithCancel(context.Background())
	defer cut()
	st, err = ProcessDocuments(ctx, m, "t", 0, func(ctx context.Context, batch []SourceDocument) (map[int64]int, error) {
		calls++
		cut()
		return nil, fmt.Errorf("extracting: %w", ctx.Err())
	}, ProcessOptions{})
	if !errors.Is(err, context.Canceled) || calls != 1 || st.Attempted != 0 || st.Failed != 0 {
		t.Fatalf("calls=%d stats=%+v err=%v; want one cut batch, not counted", calls, st, err)
	}
	for id := int64(1); id <= 2; id++ {
		if m.states[id] != DocFetched || m.tries[id] != 0 {
			t.Fatalf("doc %d: state %s tries %d, want fetched with no attempt charged", id, m.states[id], m.tries[id])
		}
	}
}

// Parallel runs the batches at once (as many as the endpoint serves) and
// records every document; an outage still stops the run on the streak.
func TestProcessDocuments_Parallel(t *testing.T) {
	restore, err := llm.SetRouting(llm.ClaudeRouting(llm.EndpointClaudeTeam, &llm.ClaudeTeamEndpoint{ProcsPerSeat: 4}, "claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	var docs []SourceDocument
	for i := int64(1); i <= 12; i++ {
		docs = append(docs, fetched(i, fmt.Sprintf("u%d", i), 1))
	}
	store := newMemDocs(docs...)
	var inFlight, peak atomic.Int32
	extract := func(ctx context.Context, b []SourceDocument) (map[int64]int, error) {
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		return map[int64]int{b[0].ID: 2}, nil
	}
	st, err := ProcessDocuments(context.Background(), store, "t", 0, extract, ProcessOptions{Parallel: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Extracted != 12 || st.Facts != 24 || peak.Load() < 2 || peak.Load() > 4 {
		t.Fatalf("stats %+v, peak %d; want 12 extracted, 24 facts, 2 to 4 at once", st, peak.Load())
	}

	store = newMemDocs(docs...)
	down := fmt.Errorf("x: %w", llm.ErrUnavailable)
	_, err = ProcessDocuments(context.Background(), store, "t", 0, func(ctx context.Context, b []SourceDocument) (map[int64]int, error) {
		time.Sleep(5 * time.Millisecond)
		return nil, down
	}, ProcessOptions{Parallel: true, MaxConsecutiveFailures: 3})
	if !errors.Is(err, ErrOutage) {
		t.Fatalf("err = %v, want the outage", err)
	}
}
