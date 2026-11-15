package core

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
)

// analyzeReplayFile is a JSONL of production cases for the stance judge (one
// per line: the fields of analyzeCase in JSON, with "partner", the key fact
// a reviewed-real contradiction pairs the new fact with, 0 when none).
// TestAnalyzeReplay runs the CURRENT judge on each, then the verifier on what
// it flags, as the insertion does (the first pair the verifier keeps ends the
// fact), and writes one JSON line per case to -analyze-replay-out. Run it on
// two versions of promptrules.ContradictionRules and compare: the pairs
// flagged (the verifier's calls), the pairs kept, and the real contradictions
// still found.
//
//	go test ./backend/core -run TestAnalyzeReplay -args -analyze-replay <file> -analyze-replay-out <file> -llm-claude-team ...
var (
	analyzeReplayFile = flag.String("analyze-replay", "", "JSONL of production cases to replay through the stance judge and the verifier")
	analyzeReplayOut  = flag.String("analyze-replay-out", "", "where TestAnalyzeReplay writes its JSONL of results")
)

type analyzeReplayCase struct {
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	KeyFacts    []analyzeKeyFact `json:"key_facts"`
	NewFact     string           `json:"new_fact"`
	NewFactDate string           `json:"new_fact_date"`
	Partner     int64            `json:"partner"`
}

type analyzeReplayResult struct {
	Name        string  `json:"name"`
	Partner     int64   `json:"partner"`
	Verdict     string  `json:"verdict"`
	Flagged     []int64 `json:"flagged"`
	VerifyCalls int     `json:"verify_calls"`
	Kept        int64   `json:"kept"`
	KeptReason  string  `json:"kept_reason,omitempty"`
	Error       string  `json:"error,omitempty"`
}

func TestAnalyzeReplay(t *testing.T) {
	if *analyzeReplayFile == "" {
		t.Skip("no -analyze-replay file")
	}
	llmtest.Setup(t)
	f, err := os.Open(*analyzeReplayFile)
	if err != nil {
		t.Fatal(err)
	}
	var cases []analyzeReplayCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var c analyzeReplayCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("parsing %s: %v", *analyzeReplayFile, err)
		}
		cases = append(cases, c)
	}
	f.Close()
	sm, err := NewStanceManagerFromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	var mu sync.Mutex
	var results []analyzeReplayResult
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cases {
			tc := tc
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				res := analyzeReplayResult{Name: tc.Name, Partner: tc.Partner}
				defer func() {
					mu.Lock()
					results = append(results, res)
					mu.Unlock()
				}()
				keyFacts := make([]StanceFactInfo, 0, len(tc.KeyFacts))
				byID := map[int64]StanceFactInfo{}
				for _, kf := range tc.KeyFacts {
					info := StanceFactInfo{ID: kf.ID, CanonicalVerb: kf.Verb, NormalizedFactText: kf.Text, Date: goldenDate(t, kf.Date)}
					keyFacts = append(keyFacts, info)
					byID[kf.ID] = info
				}
				newDate := goldenDate(t, tc.NewFactDate)
				agg, err := sm.analyzeAcrossBatches(ctx, tc.Summary, keyFacts, tc.NewFact, newDate)
				if err != nil {
					res.Error = "analyze: " + err.Error()
					return
				}
				res.Verdict = agg.PrimaryVerdict
				newFact := promptrules.Statement{Text: tc.NewFact, Date: promptrules.DateOf(newDate)}
				for _, c := range agg.Contradictions {
					res.Flagged = append(res.Flagged, c.KeyFactID)
				}
				for _, c := range agg.Contradictions {
					kf, ok := byID[c.KeyFactID]
					if !ok {
						continue
					}
					res.VerifyCalls++
					keep, reason, err := sm.VerifyContradiction(ctx, newFact, statementOf(kf), c.Reason)
					if err != nil {
						res.Error = "verify: " + err.Error()
						return
					}
					if keep {
						res.Kept, res.KeptReason = c.KeyFactID, reason
						break
					}
				}
			})
		}
	})

	var flagged, calls, kept, partners, partnerFlagged, partnerKept, failed int
	for _, r := range results {
		if r.Error != "" {
			failed++
			continue
		}
		flagged += len(r.Flagged)
		calls += r.VerifyCalls
		if r.Kept != 0 {
			kept++
		}
		if r.Partner != 0 {
			partners++
			for _, id := range r.Flagged {
				if id == r.Partner {
					partnerFlagged++
				}
			}
			if r.Kept == r.Partner {
				partnerKept++
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n=== ANALYZE REPLAY: %d cases (%d failed): %d pairs flagged, %d verifier calls, %d cases with a contradiction kept; reviewed-real pairs: %d flagged and %d kept of %d ===\n",
		len(results), failed, flagged, calls, kept, partnerFlagged, partnerKept, partners)
	if *analyzeReplayOut != "" {
		out, err := os.Create(*analyzeReplayOut)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		enc := json.NewEncoder(out)
		for _, r := range results {
			if err := enc.Encode(r); err != nil {
				t.Fatal(err)
			}
		}
	}
}
