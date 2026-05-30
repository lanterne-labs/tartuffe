package eval

import (
	"fmt"
	"sort"
	"strings"
)

// FactIDPair is the canonical (smaller, larger) lookup key for a fact pair.
type FactIDPair struct{ A, B int64 }

// PairKey returns the canonical FactIDPair for any two IDs, regardless of order.
func PairKey(a, b int64) FactIDPair {
	if a < b {
		return FactIDPair{A: a, B: b}
	}
	return FactIDPair{A: b, B: a}
}

// DetectedContradiction is one row the detector recorded between two facts.
type DetectedContradiction struct {
	FactAID int64
	FactBID int64
	Reason  string
}

// PairOutcome is a single pair's eval verdict — expected vs detected.
type PairOutcome struct {
	SpecA      string // logical id from corpus
	SpecB      string
	FactAID    int64 // DB id, 0 if mapping missing
	FactBID    int64
	Expected   string // YES or NO
	Detected   bool
	Reason     string // detector's reason, empty if not detected
	Rule       string // corpus rule slug
	Notes      string // corpus notes
	Outcome    string // "TP" | "FN" | "FP" | "TN"
	Unexpected bool   // true when detected but not in expected_pairs
}

// Report aggregates per-pair outcomes into precision/recall and per-rule breakdowns.
type Report struct {
	Outcomes    []PairOutcome
	TruePos     int
	FalseNeg    int
	FalsePos    int
	TrueNeg     int
	UnexpectedF int // flagged pairs not present in expected_pairs (subset of FalsePos)
	ByRule      map[string]*RuleStats
}

// RuleStats aggregates outcomes for a single named rule (corpus taxonomy slug).
type RuleStats struct {
	Rule     string
	Expected int
	TP       int
	FN       int
	FP       int
	TN       int
}

// Precision returns TP / (TP + FP). Returns NaN when denominator is 0.
func (r *Report) Precision() float64 {
	d := r.TruePos + r.FalsePos
	if d == 0 {
		return 0
	}
	return float64(r.TruePos) / float64(d)
}

// Recall returns TP / (TP + FN). Returns NaN when denominator is 0.
func (r *Report) Recall() float64 {
	d := r.TruePos + r.FalseNeg
	if d == 0 {
		return 0
	}
	return float64(r.TruePos) / float64(d)
}

// F1 returns 2 * P * R / (P + R). Returns 0 when both P and R are 0.
func (r *Report) F1() float64 {
	p, rc := r.Precision(), r.Recall()
	if p+rc == 0 {
		return 0
	}
	return 2 * p * rc / (p + rc)
}

// Evaluate compares the corpus's expected_pairs against the contradictions
// the detector actually recorded. factIDByCorpusID maps a corpus logical fact
// ID (e.g. "sm-indus-4") to the DB fact ID assigned at insertion.
//
// Detected pairs that are not in expected_pairs count as FalsePos with
// Unexpected=true — they represent the detector flagging something the
// corpus author didn't anticipate as a contradiction.
func Evaluate(corpus *Corpus, factIDByCorpusID map[string]int64, detected []DetectedContradiction) *Report {
	rep := &Report{ByRule: make(map[string]*RuleStats)}

	detectedSet := make(map[FactIDPair]string)
	for _, d := range detected {
		detectedSet[PairKey(d.FactAID, d.FactBID)] = d.Reason
	}
	expectedSet := make(map[FactIDPair]PairSpec)
	for _, ps := range corpus.ExpectedPairs {
		aID, okA := factIDByCorpusID[ps.A]
		bID, okB := factIDByCorpusID[ps.B]
		if !okA || !okB {
			// Mapping missing — record as an outcome with FactAID/B=0 and skip stats counting.
			rep.Outcomes = append(rep.Outcomes, PairOutcome{
				SpecA: ps.A, SpecB: ps.B, Expected: ps.Expected,
				Rule: ps.Rule, Notes: ps.Notes,
				Outcome: "MAPPING_MISSING",
			})
			continue
		}
		expectedSet[PairKey(aID, bID)] = ps
	}

	// Walk expected pairs in declaration order so the report stays readable.
	for _, ps := range corpus.ExpectedPairs {
		aID, okA := factIDByCorpusID[ps.A]
		bID, okB := factIDByCorpusID[ps.B]
		if !okA || !okB {
			continue // already accounted for above
		}
		key := PairKey(aID, bID)
		reason, detected := detectedSet[key]
		out := PairOutcome{
			SpecA: ps.A, SpecB: ps.B,
			FactAID: aID, FactBID: bID,
			Expected: ps.Expected, Rule: ps.Rule, Notes: ps.Notes,
			Detected: detected, Reason: reason,
		}
		switch {
		case ps.IsContradiction() && detected:
			out.Outcome = "TP"
			rep.TruePos++
			ruleStatsFor(rep, ps.Rule).TP++
		case ps.IsContradiction() && !detected:
			out.Outcome = "FN"
			rep.FalseNeg++
			ruleStatsFor(rep, ps.Rule).FN++
		case !ps.IsContradiction() && detected:
			out.Outcome = "FP"
			rep.FalsePos++
			ruleStatsFor(rep, ps.Rule).FP++
		default:
			out.Outcome = "TN"
			rep.TrueNeg++
			ruleStatsFor(rep, ps.Rule).TN++
		}
		ruleStatsFor(rep, ps.Rule).Expected++
		rep.Outcomes = append(rep.Outcomes, out)
	}

	// Pairs the detector flagged but the corpus didn't list as expected — each
	// counts as a false positive with Unexpected=true.
	for key, reason := range detectedSet {
		if _, ok := expectedSet[key]; ok {
			continue
		}
		out := PairOutcome{
			FactAID: key.A, FactBID: key.B,
			Expected: "NO (implicit)", Detected: true, Reason: reason,
			Outcome: "FP", Unexpected: true,
		}
		rep.FalsePos++
		rep.UnexpectedF++
		rep.Outcomes = append(rep.Outcomes, out)
	}

	return rep
}

func ruleStatsFor(rep *Report, rule string) *RuleStats {
	if rule == "" {
		rule = "(unspecified)"
	}
	rs, ok := rep.ByRule[rule]
	if !ok {
		rs = &RuleStats{Rule: rule}
		rep.ByRule[rule] = rs
	}
	return rs
}

// FormatReport returns a human-readable text rendering of the report.
func FormatReport(rep *Report) string {
	var b strings.Builder

	total := rep.TruePos + rep.FalseNeg + rep.FalsePos + rep.TrueNeg
	fmt.Fprintln(&b, "=== EVAL REPORT ===")
	fmt.Fprintf(&b, "Pairs evaluated:      %d\n", total)
	fmt.Fprintf(&b, "True  positives (TP): %d\n", rep.TruePos)
	fmt.Fprintf(&b, "False negatives (FN): %d  (missed real contradictions)\n", rep.FalseNeg)
	fmt.Fprintf(&b, "False positives (FP): %d  (unwarranted flags; %d unexpected pairs)\n", rep.FalsePos, rep.UnexpectedF)
	fmt.Fprintf(&b, "True  negatives (TN): %d\n", rep.TrueNeg)
	fmt.Fprintf(&b, "\nPrecision: %.3f\n", rep.Precision())
	fmt.Fprintf(&b, "Recall:    %.3f\n", rep.Recall())
	fmt.Fprintf(&b, "F1:        %.3f\n", rep.F1())

	rules := make([]string, 0, len(rep.ByRule))
	for r := range rep.ByRule {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	if len(rules) > 0 {
		fmt.Fprintln(&b, "\n--- per rule (Expected | TP | FN | FP | TN) ---")
		for _, r := range rules {
			s := rep.ByRule[r]
			fmt.Fprintf(&b, "%-32s  %d | %d | %d | %d | %d\n", r, s.Expected, s.TP, s.FN, s.FP, s.TN)
		}
	}

	if len(rep.Outcomes) > 0 {
		fmt.Fprintln(&b, "\n--- pair outcomes ---")
		for _, o := range rep.Outcomes {
			if o.Outcome == "TP" || o.Outcome == "TN" {
				fmt.Fprintf(&b, "[%s] %s ↔ %s   %s\n", o.Outcome, o.SpecA, o.SpecB, o.Rule)
				continue
			}
			label := o.SpecA + " ↔ " + o.SpecB
			if o.Unexpected {
				label = fmt.Sprintf("fact_id=%d ↔ fact_id=%d (UNEXPECTED)", o.FactAID, o.FactBID)
			}
			fmt.Fprintf(&b, "[%s] %s   %s\n     notes: %s\n     detector: %s\n", o.Outcome, label, o.Rule, o.Notes, o.Reason)
		}
	}
	return b.String()
}
