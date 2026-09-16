package core

import (
	"fmt"
	"reflect"
	"testing"
)

// Facts with the same embedding_text share one group (the first fact is the
// representative); facts without embedding_text each get their own group.
func TestClusterFactsByExactLabel(t *testing.T) {
	facts := []StanceRoutingFact{
		{ID: 1, EmbeddingText: "retraites"},
		{ID: 2, EmbeddingText: "immigration"},
		{ID: 3, EmbeddingText: "retraites"},
		{ID: 4, EmbeddingText: ""},
		{ID: 5, EmbeddingText: ""},
	}
	got := clusterFactsByExactLabel(facts)

	if len(got) != 4 {
		t.Fatalf("want 4 groups, got %d: %v", len(got), got)
	}
	if ids := factIDs(got["retraites"]); !reflect.DeepEqual(ids, []int64{1, 3}) {
		t.Errorf("retraites group = %v, want [1 3] (input order kept, first is the representative)", ids)
	}
	if ids := factIDs(got["immigration"]); !reflect.DeepEqual(ids, []int64{2}) {
		t.Errorf("immigration group = %v, want [2]", ids)
	}
	for _, id := range []int64{4, 5} {
		key := fmt.Sprintf("unnamed-fact-%d", id)
		if ids := factIDs(got[key]); !reflect.DeepEqual(ids, []int64{id}) {
			t.Errorf("unlabelled fact %d must be alone under %q, got %v", id, key, ids)
		}
	}
}

func factIDs(facts []StanceRoutingFact) []int64 {
	ids := make([]int64, len(facts))
	for i, f := range facts {
		ids[i] = f.ID
	}
	return ids
}
