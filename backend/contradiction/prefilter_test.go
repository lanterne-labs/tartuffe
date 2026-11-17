package contradiction

import (
	"context"
	"errors"
	"testing"

	"digital-mirror/backend/pairfilter"
)

// clearAll clears every pair it is asked about and counts them.
type clearAll struct {
	asked int
	fail  bool
}

func (c *clearAll) Name() string { return "clear all" }
func (c *clearAll) Keep(_ context.Context, pairs []pairfilter.Pair) ([]bool, error) {
	if c.fail {
		return nil, errors.New("down")
	}
	c.asked += len(pairs)
	return make([]bool, len(pairs)), nil
}

func TestScreenPairs(t *testing.T) {
	pairs := []pairInfo{
		{factAID: 1, factBID: 2, similarity: 0.5},
		{factAID: 3, factBID: 4, similarity: 0.5, queued: true},       // a producer asked for it
		{factAID: 5, factBID: 6, similarity: groupingSimFloor + 0.01}, // the grouping check reads it
		{factAID: 7, factBID: 8, similarity: 0.6},
	}

	f := &clearAll{}
	toJudge, cleared := screenPairs(context.Background(), f, pairs)
	if f.asked != 2 || len(cleared) != 2 || cleared[0].factAID != 1 || cleared[1].factAID != 7 {
		t.Fatalf("want pairs 1 and 7 cleared, got asked=%d cleared=%v", f.asked, cleared)
	}
	if len(toJudge) != 2 || toJudge[0].factAID != 3 || toJudge[1].factAID != 5 {
		t.Fatalf("the queued and the near-identical pair go to the judge, got %v", toJudge)
	}

	// A filter that does not answer clears nothing.
	toJudge, cleared = screenPairs(context.Background(), &clearAll{fail: true}, pairs)
	if len(toJudge) != len(pairs) || len(cleared) != 0 {
		t.Fatalf("a failing filter must leave every pair to the judge, got %d to judge, %d cleared", len(toJudge), len(cleared))
	}

	// No filter: the pairs as they are.
	toJudge, cleared = screenPairs(context.Background(), pairfilter.All{}, pairs)
	if len(toJudge) != len(pairs) || cleared != nil {
		t.Fatalf("no filter: got %d to judge, %d cleared", len(toJudge), len(cleared))
	}
}
