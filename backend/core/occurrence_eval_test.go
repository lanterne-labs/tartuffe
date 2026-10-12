//go:build llmeval

package core

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/llmtest"
)

// TestOccurrenceGateEval measures the grouping rule on 207 prod pairs that the
// old text-only gate grouped (testdata/occurrence_eval_pairs.json). Each pair
// carries its quotes, dates and outlets, and a label made with that evidence:
// "same" or "bundle" (one occurrence, groupable) or "different", with "sure"
// false on borderline pairs. The pairs that meet the evidence rule go to
// IsSameOccurrence; the test logs a scorecard and fails only on gate errors.
//
// It is the measure to compare models, so it runs only on demand (build tag
// llmeval) and takes about 2 h with thinking:
//
//	make llm-eval
//	make llm-eval LLM_API=http://127.0.0.1:8095/v1 LLM_MODEL=candidate
//
// Reference (2026-10-06), on the 164 pairs that meet the evidence rule:
//
//	model                          found  wrong  sure-wrong
//	Gemma 4 26B-A4B, no thinking   98 %   17 %   5 %
//	Qwen3.8-27B Q3, no thinking    87 %    8 %   2 %
//	Qwen3.8-27B Q3, thinking       95 %   10 %   2 %
//
// On 2026-10-11, Opus 5.5 at effort high labelled every pair again, and a
// review of each change of group kept 10 new labels. With them (the Qwen rows
// above use the old labels):
//
//	model                          found  wrong  sure-wrong
//	Sonnet 5.5, effort medium      95 %    5 %   1 %
//	Opus 5.5, effort medium        95 %    1 %   0 %
//
// TestOccurrenceRelabel in occurrence_relabel_test.go runs the labelling pass.
func TestOccurrenceGateEval(t *testing.T) {
	llmtest.Setup(t)
	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	pairs := loadOccurrenceEvalPairs(t)

	ctx, cancel := llmtest.Context(t)
	defer cancel()
	// The gate calls run first, all pairs at once with -llm-claude (one
	// batch). The scorecard reads their results by index.
	type gated struct {
		meetsRule bool
		x, y      ClaimEvidence
		got       OccurrenceVerdict
		err       error
	}
	results := make([]gated, len(pairs))
	for i, p := range pairs {
		r := &results[i]
		r.meetsRule = p.meetsRule()
		if r.meetsRule {
			r.x, r.y = p.evidence(t)
		}
	}
	llmtest.ForEach(len(pairs), func(i int) {
		r := &results[i]
		if r.meetsRule {
			r.got, r.err = adj.IsSameOccurrence(ctx, r.x, r.y)
		}
	})

	var judged, truth, grouped, found, wrong, sureWrong, gateErrors int
	for i, p := range pairs {
		if !results[i].meetsRule {
			continue
		}
		judged++
		groupable := p.Label != "different"
		if groupable {
			truth++
		}
		got, err := results[i].got, results[i].err
		if err != nil {
			gateErrors++
			t.Logf("%s: gate error: %v", p.Name, err)
			continue
		}
		if !got.Groupable() {
			if groupable {
				t.Logf("%s: MISSED GROUP (label %s, sure %v)\n  A: %s\n  B: %s", p.Name, p.Label, p.Sure, p.FactA, p.FactB)
			}
			continue
		}
		grouped++
		switch {
		case groupable:
			found++
		case p.Sure:
			wrong++
			sureWrong++
		default:
			wrong++
		}
		if !groupable {
			t.Logf("%s: WRONG GROUP (label %s, sure %v)\n  A: %s\n  B: %s", p.Name, p.Label, p.Sure, p.FactA, p.FactB)
		}
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	t.Logf("=== OCCURRENCE EVAL: %d pairs judged; found %d/%d true groups (%.0f %%); %d grouped, wrong %d (%.0f %%), sure-wrong %d (%.0f %%); gate errors %d ===",
		judged, found, truth, pct(found, truth), grouped, wrong, pct(wrong, grouped), sureWrong, pct(sureWrong, grouped), gateErrors)
	if gateErrors > 0 {
		t.Errorf("%d gate errors: the model must answer every pair", gateErrors)
	}
}

// occurrenceEvalPair is one labelled pair of testdata/occurrence_eval_pairs.json.
type occurrenceEvalPair struct {
	Name            string                 `json:"name"`
	Label           string                 `json:"label"`
	Sure            bool                   `json:"sure"`
	DateGapDays     *int                   `json:"date_gap_days"`
	QuoteSimilarity float64                `json:"quote_similarity"`
	FactA           string                 `json:"fact_a"`
	SourcesA        []occurrenceEvalSource `json:"sources_a"`
	FactB           string                 `json:"fact_b"`
	SourcesB        []occurrenceEvalSource `json:"sources_b"`
}

type occurrenceEvalSource struct {
	Outlet string `json:"outlet"`
	Date   string `json:"date"`
	Quote  string `json:"quote"`
}

func loadOccurrenceEvalPairs(t *testing.T) []occurrenceEvalPair {
	t.Helper()
	raw, err := os.ReadFile("testdata/occurrence_eval_pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	var pairs []occurrenceEvalPair
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	return pairs
}

// meetsRule reports whether the pair meets the evidence rule of the grouping.
func (p occurrenceEvalPair) meetsRule() bool {
	return p.QuoteSimilarity >= groupQuoteSimilarityFloor ||
		(p.DateGapDays != nil && *p.DateGapDays <= groupMaxDateGapDays)
}

// evidence returns the claim evidence of the two facts of the pair.
func (p occurrenceEvalPair) evidence(t *testing.T) (ClaimEvidence, ClaimEvidence) {
	t.Helper()
	of := func(text string, sources []occurrenceEvalSource) ClaimEvidence {
		e := ClaimEvidence{Text: text}
		for _, s := range sources {
			cs := ClaimSource{Outlet: s.Outlet, Quote: s.Quote}
			if s.Date != "" {
				d, err := time.Parse("2006-01-02", s.Date)
				if err != nil {
					t.Fatalf("bad date %q: %v", s.Date, err)
				}
				cs.PublishedAt = &d
			}
			e.Sources = append(e.Sources, cs)
		}
		return e
	}
	return of(p.FactA, p.SourcesA), of(p.FactB, p.SourcesB)
}
