//go:build llmeval

package contradiction

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
)

// detectEvalPair is one pair of testdata/detect_eval_pairs.json: 60 prod pairs
// sampled 2026-10-07 from 200 labelled ones (24 contradictions, 26 false
// contradictions that production kept, 10 pairs the detector rejected), each
// with the other reports of its two statements. Independent reviewers
// labelled them with the rulebook (promptrules) and the owner's precision
// rule, without seeing which side a pair came from.
type detectEvalPair struct {
	Name          string   `json:"name"`
	Label         string   `json:"label"` // "contradiction" or "no"
	Sure          bool     `json:"sure"`
	Prod          string   `json:"prod"` // "kept" or "rejected" by production
	FlaggedReason string   `json:"flagged_reason"`
	TextA         string   `json:"text_a"`
	DateA         string   `json:"date_a"`
	AlsoA         []string `json:"also_a"`
	TextB         string   `json:"text_b"`
	DateB         string   `json:"date_b"`
	AlsoB         []string `json:"also_b"`
}

func loadDetectEvalPairs(t *testing.T) []detectEvalPair {
	t.Helper()
	raw, err := os.ReadFile("testdata/detect_eval_pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	var pairs []detectEvalPair
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	return pairs
}

func evalDate(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return &d
}

// TestDetectEval measures the batch contradiction detector on the 60 pairs.
// It logs a scorecard on the pairs labelled "sure" and on all pairs; it fails
// only on detector errors.
//
// It runs only on demand (build tag llmeval):
//
//	make llm-eval
//	make llm-eval LLM_THINK=occurrence,detect
func TestDetectEval(t *testing.T) {
	llmtest.Setup(t)
	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	pairs := loadDetectEvalPairs(t)
	date := func(s string) *time.Time { return evalDate(t, s) }

	type score struct{ truePos, falsePos, falseNeg, trueNeg int }
	var sure, all score
	add := func(s *score, want, got bool) {
		switch {
		case want && got:
			s.truePos++
		case !want && got:
			s.falsePos++
		case want && !got:
			s.falseNeg++
		default:
			s.trueNeg++
		}
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()
	errorsSeen := 0
	for _, p := range pairs {
		r := detector.AreContradictoryBatch(ctx, []FactPair{{
			TextA: p.TextA, DateA: date(p.DateA), AlsoA: p.AlsoA,
			TextB: p.TextB, DateB: date(p.DateB), AlsoB: p.AlsoB,
		}})[0]
		if r.Error != nil {
			errorsSeen++
			t.Logf("%s: detector error: %v", p.Name, r.Error)
			continue
		}
		want := p.Label == "contradiction"
		add(&all, want, r.IsContradictory)
		if p.Sure {
			add(&sure, want, r.IsContradictory)
		}
		if want != r.IsContradictory {
			t.Logf("%s: label %s (sure %v), detector %v: %s", p.Name, p.Label, p.Sure, r.IsContradictory, r.Reason)
		}
	}
	report := func(name string, s score) {
		pct := func(a, b int) float64 {
			if b == 0 {
				return 0
			}
			return 100 * float64(a) / float64(b)
		}
		t.Logf("=== DETECT EVAL (%s): precision %.0f %% (%d/%d flagged), recall %.0f %% (%d/%d contradictions), %d false positives, %d missed ===",
			name, pct(s.truePos, s.truePos+s.falsePos), s.truePos, s.truePos+s.falsePos,
			pct(s.truePos, s.truePos+s.falseNeg), s.truePos, s.truePos+s.falseNeg, s.falsePos, s.falseNeg)
	}
	report("sure labels", sure)
	report("all labels", all)
	if errorsSeen > 0 {
		t.Errorf("%d detector errors: the model must answer every pair", errorsSeen)
	}
}

// TestAuditEval measures the cleanup of existing contradictions on the 50
// pairs that production kept: 24 real contradictions and 26 false ones. For
// each pair it asks the auditor (one Audit call, its own majority vote) and
// the verify judge. A contradiction is removed in production only when the
// audit says remove and the verifier does not keep it (applyAuditRemoval), so
// the scorecard counts that combination: false ones removed (want all), real
// ones removed (want none).
func TestAuditEval(t *testing.T) {
	llmtest.Setup(t)
	auditor, err := NewOpenAIContradictionAuditorFromEnv()
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}
	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	var falseRemoved, falseTotal, realRemoved, realTotal, sureFalseRemoved, sureFalse, sureRealRemoved, sureReal, errorsSeen int
	for _, p := range loadDetectEvalPairs(t) {
		if p.Prod != "kept" {
			continue
		}
		a := promptrules.Statement{Text: p.TextA, Also: p.AlsoA}
		b := promptrules.Statement{Text: p.TextB, Also: p.AlsoB}
		res := auditor.Audit(ctx, a, b, evalDate(t, p.DateA), evalDate(t, p.DateB), p.FlaggedReason)
		keep, verifyReason, err := detector.VerifyContradiction(ctx, a, b, p.FlaggedReason)
		if err != nil {
			errorsSeen++
			t.Logf("%s: verify error: %v", p.Name, err)
			continue
		}
		removed := res.Verdict == VerdictRemove && !keep
		real := p.Label == "contradiction"
		if real {
			realTotal++
			if p.Sure {
				sureReal++
			}
			if removed {
				realRemoved++
				if p.Sure {
					sureRealRemoved++
				}
			}
		} else {
			falseTotal++
			if p.Sure {
				sureFalse++
			}
			if removed {
				falseRemoved++
				if p.Sure {
					sureFalseRemoved++
				}
			}
		}
		if removed == real {
			t.Logf("%s: label %s (sure %v): audit %s (%s), verify keep %v: %s | %s", p.Name, p.Label, p.Sure, res.Verdict, res.Rule, keep, res.Reasoning, verifyReason)
		}
	}
	t.Logf("=== AUDIT EVAL: false contradictions removed %d/%d (sure %d/%d); real contradictions removed %d/%d (sure %d/%d); verify errors %d ===",
		falseRemoved, falseTotal, sureFalseRemoved, sureFalse, realRemoved, realTotal, sureRealRemoved, sureReal, errorsSeen)
	if errorsSeen > 0 {
		t.Errorf("%d verify errors: the model must answer every pair", errorsSeen)
	}
}
