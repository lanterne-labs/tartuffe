package metrics

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeSink struct {
	got  [][]DecisionCount
	fail bool
}

func (f *fakeSink) AddDecisionCounts(ctx context.Context, counts []DecisionCount) error {
	if f.fail {
		return errors.New("db down")
	}
	sorted := append([]DecisionCount(nil), counts...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Stage != sorted[j].Stage {
			return sorted[i].Stage < sorted[j].Stage
		}
		return sorted[i].Outcome < sorted[j].Outcome
	})
	f.got = append(f.got, sorted)
	return nil
}

// Tallies accumulate in memory, flush as one batch, and a flush that fails
// keeps the counts for the next one — a judgement is never lost to a
// database blip.
func TestDecisionsAccumulateAndFlush(t *testing.T) {
	takeDecisions() // start from empty

	RecordDecision(StageFactDedupe, OutcomeDuplicate)
	RecordDecision(StageFactDedupe, OutcomeDuplicate)
	AddDecision(StageFactDedupe, OutcomeDistinct, 3)
	AddDecision(StageEntityGate, OutcomeEntityRejected, 0) // zero is not a decision

	sink := &fakeSink{}
	if err := FlushDecisions(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	want := []DecisionCount{
		{Stage: StageFactDedupe, Outcome: OutcomeDistinct, Count: 3},
		{Stage: StageFactDedupe, Outcome: OutcomeDuplicate, Count: 2},
	}
	if len(sink.got) != 1 || len(sink.got[0]) != 2 || sink.got[0][0] != want[0] || sink.got[0][1] != want[1] {
		t.Fatalf("flushed %+v, want %+v", sink.got, want)
	}

	// Nothing left to flush.
	if err := FlushDecisions(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("an empty flush must not write: %+v", sink.got)
	}

	// A failed flush keeps the counts.
	RecordDecision(StageStanceAssign, OutcomeStanceNew)
	failing := &fakeSink{fail: true}
	if err := FlushDecisions(context.Background(), failing); err == nil {
		t.Fatal("want an error from a failing sink")
	}
	if err := FlushDecisions(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	last := sink.got[len(sink.got)-1]
	if len(last) != 1 || last[0] != (DecisionCount{Stage: StageStanceAssign, Outcome: OutcomeStanceNew, Count: 1}) {
		t.Fatalf("the failed flush lost counts: %+v", last)
	}
}

type fakeReader struct{ allTime, today []DecisionCount }

func (f fakeReader) DecisionTotals(context.Context) ([]DecisionCount, []DecisionCount, error) {
	return f.allTime, f.today, nil
}

// The gauges mirror the database totals, and a stage that stops appearing is
// dropped instead of freezing at its last value.
func TestDecisionGauges(t *testing.T) {
	refreshDecisionGauges(context.Background(), fakeReader{
		allTime: []DecisionCount{{Stage: StageFactDedupe, Outcome: OutcomeDuplicate, Count: 12}},
		today:   []DecisionCount{{Stage: StageFactDedupe, Outcome: OutcomeDuplicate, Count: 5}},
	})
	if got := testutil.ToFloat64(DecisionsTotal.WithLabelValues(StageFactDedupe, OutcomeDuplicate)); got != 12 {
		t.Fatalf("all-time gauge = %v, want 12", got)
	}
	if got := testutil.ToFloat64(DecisionsDaily.WithLabelValues(StageFactDedupe, OutcomeDuplicate)); got != 5 {
		t.Fatalf("today gauge = %v, want 5", got)
	}

	refreshDecisionGauges(context.Background(), fakeReader{
		allTime: []DecisionCount{{Stage: StageEntityGate, Outcome: OutcomeEntityKept, Count: 1}},
	})
	if n := testutil.CollectAndCount(DecisionsDaily); n != 0 {
		t.Fatalf("today gauges = %d series, want 0 after an empty day", n)
	}
	if n := testutil.CollectAndCount(DecisionsTotal); n != 1 {
		t.Fatalf("all-time gauges = %d series, want only the current one", n)
	}
}
