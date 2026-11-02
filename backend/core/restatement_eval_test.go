package core

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llmtest"
)

// restatementEval is a JSONL of labelled pairs of contradictions sharing a
// fact (row1, row2, person, shared, other1/date1/reason1, other2/date2/
// reason2, label same|different). TestRestatementEval runs the restatement
// judge on each and reports its agreement with the labels: a "different"
// pair judged same would hide a contradiction, the costly error.
//
//	go test ./backend/core -run TestRestatementEval -args -restatement-eval <file> -llm-claude ...
var restatementEval = flag.String("restatement-eval", "", "JSONL of labelled pairs of contradictions sharing a fact")

type restatementCase struct {
	Row1    int64  `json:"row1"`
	Row2    int64  `json:"row2"`
	Person  string `json:"person"`
	Shared  string `json:"shared"`
	Other1  string `json:"other1"`
	Date1   string `json:"date1"`
	Reason1 string `json:"reason1"`
	Other2  string `json:"other2"`
	Date2   string `json:"date2"`
	Reason2 string `json:"reason2"`
	Label   string `json:"label"`
}

func evalDate(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

func TestRestatementEval(t *testing.T) {
	if *restatementEval == "" {
		t.Skip("no -restatement-eval file")
	}
	llmtest.Setup(t)
	f, err := os.Open(*restatementEval)
	if err != nil {
		t.Fatal(err)
	}
	var cases []restatementCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var c restatementCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c)
	}
	f.Close()

	judge, err := NewRestatementJudgeFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	var mu sync.Mutex
	m := map[[2]string]int{} // (label, judged) -> count
	var misses []string
	llmtest.Group(t, func(t *testing.T) {
		for _, c := range cases {
			c := c
			t.Run(fmt.Sprintf("%d_%d", c.Row1, c.Row2), func(t *testing.T) {
				llmtest.Parallel(t)
				same, why, err := judge.SameContradiction(ctx, c.Person, SharedContradiction{
					SharedText: c.Shared,
					OtherText:  c.Other1, OtherDate: evalDate(c.Date1), Reason: c.Reason1,
					NewText: c.Other2, NewDate: evalDate(c.Date2), NewReason: c.Reason2,
				})
				if err != nil {
					t.Fatalf("judge: %v", err)
				}
				judged := "different"
				if same {
					judged = "same"
				}
				mu.Lock()
				m[[2]string{c.Label, judged}]++
				if judged != c.Label {
					misses = append(misses, fmt.Sprintf("%d/%d label=%s judged=%s: %s", c.Row1, c.Row2, c.Label, judged, why))
				}
				mu.Unlock()
			})
		}
	})
	for _, l := range []string{"same", "different"} {
		t.Logf("label %-9s: judged same %3d, different %3d", l, m[[2]string{l, "same"}], m[[2]string{l, "different"}])
	}
	for _, s := range misses {
		t.Log("miss: " + s)
	}
}
