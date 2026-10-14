package core

import (
	"context"
	"errors"
	"testing"

	"digital-mirror/backend/llm"
)

// TestApplyStanceDecision: the apply step writes the decision and makes no
// LLM call. The label and summary stay as they are: the new membership is
// pending for the refresh step. An invalid decision writes nothing.
func TestApplyStanceDecision(t *testing.T) {
	noLLM := func(prompt string) any {
		t.Errorf("ApplyStanceDecision called the LLM: %.80s", prompt)
		return nil
	}
	ctx := context.Background()
	const vote = "Jean Dupont a voté POUR le texte de loi : « Fixe l'âge de départ à 64 ans »"

	t.Run("new info joins the stance", func(t *testing.T) {
		storer := pensionStorer()
		sm, _ := newOutageManager(t, storer, noLLM)
		res, err := sm.ApplyStanceDecision(ctx, 1, 20, vote, []float32{0, 1}, StanceDecision{StanceID: 1, Verdict: VerdictNewInfo})
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != VerdictNewInfo || res.StanceID != 1 {
			t.Fatalf("result = %+v, want NEW_INFO in stance 1", res)
		}
		doc := storer.stances[1]
		if len(storer.facts[1]) != 2 || doc.FactCount != 2 || len(doc.KeyFactIDs) != 2 || doc.KeyFactIDs[1] != 20 {
			t.Errorf("facts=%+v fact_count=%d key_facts=%v, want fact 20 joined", storer.facts[1], doc.FactCount, doc.KeyFactIDs)
		}
		if doc.ClusterLabel != "retraites" || doc.Summary != "âge de départ" || len(storer.consumed) != 0 {
			t.Errorf("label=%q summary=%q refreshes=%v: the refresh must stay pending", doc.ClusterLabel, doc.Summary, storer.consumed)
		}
	})

	t.Run("a contradiction records its entries", func(t *testing.T) {
		storer := pensionStorer()
		sm, _ := newOutageManager(t, storer, noLLM)
		d := StanceDecision{StanceID: 1, Verdict: VerdictContradiction, Contradictions: []StanceContradictionEntry{{KeyFactID: 10, Reason: "62 contre 64"}}}
		if _, err := sm.ApplyStanceDecision(ctx, 1, 20, vote, []float32{0, 1}, d); err != nil {
			t.Fatal(err)
		}
		if len(storer.contradictions) != 1 || storer.contradictions[0].factBID != 10 || storer.stances[1].FactCount != 2 {
			t.Errorf("contradictions=%+v fact_count=%d", storer.contradictions, storer.stances[1].FactCount)
		}
	})

	t.Run("a new stance takes the label", func(t *testing.T) {
		storer := pensionStorer()
		sm, _ := newOutageManager(t, storer, noLLM)
		res, err := sm.ApplyStanceDecision(ctx, 1, 20, vote, []float32{0, 1}, StanceDecision{Verdict: VerdictNewStance, NewLabel: "politique des retraites"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != VerdictNewStance || storer.stances[res.StanceID].ClusterLabel != "politique des retraites" || len(storer.facts[res.StanceID]) != 1 {
			t.Errorf("result=%+v stances=%+v", res, storer.stances)
		}
	})

	for name, tc := range map[string]struct {
		personID int64
		d        StanceDecision
	}{
		"new stance without a label":     {1, StanceDecision{Verdict: VerdictNewStance, NewLabel: "  "}},
		"new stance with an entry":       {1, StanceDecision{Verdict: VerdictNewStance, NewLabel: "x", Contradictions: []StanceContradictionEntry{{KeyFactID: 10}}}},
		"contradiction without an entry": {1, StanceDecision{StanceID: 1, Verdict: VerdictContradiction}},
		"new info with an entry":         {1, StanceDecision{StanceID: 1, Verdict: VerdictNewInfo, Contradictions: []StanceContradictionEntry{{KeyFactID: 10}}}},
		"unknown verdict":                {1, StanceDecision{StanceID: 1, Verdict: "MAYBE"}},
		"stance of another person":       {2, StanceDecision{StanceID: 1, Verdict: VerdictNewInfo}},
		"stance that does not exist":     {1, StanceDecision{StanceID: 99, Verdict: VerdictNewInfo}},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			storer := pensionStorer()
			sm, _ := newOutageManager(t, storer, noLLM)
			if _, err := sm.ApplyStanceDecision(ctx, tc.personID, 20, vote, []float32{0, 1}, tc.d); err == nil {
				t.Fatal("want an error")
			}
			if len(storer.stances) != 1 || len(storer.facts[1]) != 1 || len(storer.contradictions) != 0 {
				t.Errorf("stances=%d facts=%+v contradictions=%v: nothing may be written", len(storer.stances), storer.facts[1], storer.contradictions)
			}
		})
	}
}

// TestAssignStance: the assignment alone makes one topic call, no analysis,
// and writes nothing.
func TestAssignStance(t *testing.T) {
	storer := pensionStorer()
	sm, o := newOutageManager(t, storer, answers(nil))
	d, err := sm.AssignStance(context.Background(), 1, 20, "Jean Dupont a voté POUR le report de l'âge de départ", []float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if d.Verdict != VerdictNewInfo || d.StanceID != 1 {
		t.Fatalf("decision = %+v, want NEW_INFO in stance 1", d)
	}
	if n, a := o.count(topicPrompt), o.count(analyzePrompt); n != 1 || a != 0 {
		t.Errorf("topic calls=%d analyze calls=%d, want 1 and 0", n, a)
	}
	if len(storer.facts[1]) != 1 {
		t.Errorf("facts=%+v: the assignment must write nothing", storer.facts[1])
	}
}

// pendingLister lists fixed stances as pending refresh.
type pendingLister []int64

func (p pendingLister) ListStancesPendingRefresh(_ context.Context, limit int) ([]int64, error) {
	return p[:min(limit, len(p))], nil
}

// TestRefreshPendingStances: the step refreshes the listed stances within
// its budget, and an unavailable LLM stops it with the error.
func TestRefreshPendingStances(t *testing.T) {
	ctx := context.Background()
	storer := pensionStorer()
	sm, _ := newOutageManager(t, storer, answers(nil))
	n, err := RefreshPendingStances(ctx, pendingLister{1}, sm, 5)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want 1 stance refreshed", n, err)
	}
	if storer.stances[1].Summary != "refreshed" || len(storer.consumed) != 1 {
		t.Errorf("summary=%q refreshes=%v", storer.stances[1].Summary, storer.consumed)
	}

	storer = pensionStorer()
	sm, _ = newOutageManager(t, storer, func(string) any { return nil })
	n, err = RefreshPendingStances(ctx, pendingLister{1}, sm, 5)
	if !errors.Is(err, llm.ErrUnavailable) || n != 0 {
		t.Fatalf("n=%d err=%v, want ErrUnavailable", n, err)
	}
	if storer.stances[1].Summary != "âge de départ" {
		t.Errorf("summary = %q, want the previous one", storer.stances[1].Summary)
	}

	// Another failure (a reply that is not JSON) is logged; the step goes on
	// with the next stance and returns no error.
	storer = pensionStorer()
	storer.stances[2] = StanceDocument{ID: 2, PersonID: 1, ClusterLabel: "impôts", Summary: "taxe", ClusterEmbedding: []float32{0, 1}, FactCount: 1}
	storer.facts[2] = []StanceFactInfo{{ID: 11, NormalizedFactText: "veut baisser la TVA"}}
	sm, o := newOutageManager(t, storer, func(string) any { return "not json" })
	n, err = RefreshPendingStances(ctx, pendingLister{1, 2}, sm, 5)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2 stances tried and no error", n, err)
	}
	if len(o.prompts) != 2 || storer.stances[1].Summary != "âge de départ" || storer.stances[2].Summary != "taxe" {
		t.Errorf("calls=%d summaries=%q,%q: both stances tried, both keep their summary", len(o.prompts), storer.stances[1].Summary, storer.stances[2].Summary)
	}
}
