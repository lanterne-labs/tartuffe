package contradiction

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
)

// replayFile is a JSONL of reviewed contradictions (one per line: id,
// fact_a, date_a, fact_b, date_b, reason, verdict). TestReplayReviewed runs
// the CURRENT verifier on each and reports, per review verdict, how many it
// keeps and drops: a false row it keeps is a reason that would come back if
// the pair were detected again, a real row it drops is a contradiction the
// rules now miss. /audit writes the file from contradiction_reviews.
//
//	go test ./backend/contradiction -run TestReplayReviewed -args -replay <file> -llm-claude ...
var replayFile = flag.String("replay", "", "JSONL of reviewed contradictions to replay through the verifier")

type replayRow struct {
	ID      int64  `json:"id"`
	Person  string `json:"person"`
	FactA   string `json:"fact_a"`
	DateA   string `json:"date_a"`
	FactB   string `json:"fact_b"`
	DateB   string `json:"date_b"`
	Reason  string `json:"reason"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
}

func TestReplayReviewed(t *testing.T) {
	if *replayFile == "" {
		t.Skip("no -replay file")
	}
	llmtest.Setup(t)
	f, err := os.Open(*replayFile)
	if err != nil {
		t.Fatal(err)
	}
	var rows []replayRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r replayRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("parsing %s: %v", *replayFile, err)
		}
		rows = append(rows, r)
	}
	f.Close()

	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	type outcome struct {
		row    replayRow
		keep   bool
		reason string
	}
	var mu sync.Mutex
	var out []outcome
	llmtest.Group(t, func(t *testing.T) {
		for _, r := range rows {
			r := r
			t.Run(fmt.Sprintf("%d", r.ID), func(t *testing.T) {
				llmtest.Parallel(t)
				keep, reason, err := detector.VerifyContradiction(ctx,
					promptrules.Statement{Text: r.FactA, Date: r.DateA},
					promptrules.Statement{Text: r.FactB, Date: r.DateB}, r.Reason)
				if err != nil {
					t.Fatalf("verify %d: %v", r.ID, err)
				}
				mu.Lock()
				out = append(out, outcome{r, keep, reason})
				mu.Unlock()
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].row.ID < out[j].row.ID })
	counts := map[string][2]int{}
	res, err := os.Create(*replayFile + ".result.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	enc := json.NewEncoder(res)
	for _, o := range out {
		c := counts[o.row.Verdict]
		if o.keep {
			c[0]++
		} else {
			c[1]++
		}
		counts[o.row.Verdict] = c
		_ = enc.Encode(map[string]any{"id": o.row.ID, "review": o.row.Verdict, "keep": o.keep, "verifier": o.reason, "note": o.row.Note})
	}
	for _, v := range []string{"real", "debatable", "false"} {
		c := counts[v]
		t.Logf("review %-9s: verifier keeps %3d, drops %3d", v, c[0], c[1])
	}
}
