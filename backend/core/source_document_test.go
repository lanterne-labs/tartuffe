package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
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
func (m *memDocs) ListUnfinishedDocuments(ctx context.Context, sourceType string, personID int64, limit int) ([]SourceDocument, error) {
	var out []SourceDocument
	for _, d := range m.docs {
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
