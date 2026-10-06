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
func TestOccurrenceGateEval(t *testing.T) {
	llmtest.Setup(t)
	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	type source struct {
		Outlet string `json:"outlet"`
		Date   string `json:"date"`
		Quote  string `json:"quote"`
	}
	var pairs []struct {
		Name            string   `json:"name"`
		Label           string   `json:"label"`
		Sure            bool     `json:"sure"`
		DateGapDays     *int     `json:"date_gap_days"`
		QuoteSimilarity float64  `json:"quote_similarity"`
		FactA           string   `json:"fact_a"`
		SourcesA        []source `json:"sources_a"`
		FactB           string   `json:"fact_b"`
		SourcesB        []source `json:"sources_b"`
	}
	raw, err := os.ReadFile("testdata/occurrence_eval_pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	evidence := func(text string, sources []source) ClaimEvidence {
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

	ctx, cancel := llmtest.Context(t)
	defer cancel()
	var judged, truth, grouped, found, wrong, sureWrong, gateErrors int
	for _, p := range pairs {
		meetsRule := p.QuoteSimilarity >= groupQuoteSimilarityFloor ||
			(p.DateGapDays != nil && *p.DateGapDays <= groupMaxDateGapDays)
		if !meetsRule {
			continue
		}
		judged++
		groupable := p.Label != "different"
		if groupable {
			truth++
		}
		got, err := adj.IsSameOccurrence(ctx, evidence(p.FactA, p.SourcesA), evidence(p.FactB, p.SourcesB))
		if err != nil {
			gateErrors++
			t.Logf("%s: gate error: %v", p.Name, err)
			continue
		}
		if !got.Groupable() {
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
