//go:build llmeval

package core

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/llmtest"
)

var relabelOut = flag.String("occurrence-relabel-out", "", "TestOccurrenceRelabel: JSON file for the new labels")

// occurrenceLabelFormat replaces the response format of the gate: a labeller
// also says whether the evidence decides the label.
const occurrenceLabelFormat = `
You label a test set for the rules above. Read the quotes, dates and outlets with care. Set "sure" to false when the evidence does not decide between two labels.
Return ONLY strict JSON, nothing else, with the keys in this order:
{"occasion_a": "<what the person said or did in A, and when/where, from the evidence>", "occasion_b": "<the same for B>", "reasoning": "<the deciding evidence>", "label": "same" | "bundle" | "different", "sure": true | false}`

// TestOccurrenceRelabel labels every pair of the grouping eval again with the
// rules of the gate, and writes the labels next to the old ones. A person
// reads each pair where the two disagree, and keeps the label that the
// evidence supports. Run it with a strong model, for example:
//
//	go test -tags llmeval -run TestOccurrenceRelabel ./backend/core -args \
//	  -llm-claude=claude-opus-5-5 -llm-claude-models=occurrence=claude-opus-5-5:high \
//	  -occurrence-relabel-out=/tmp/relabel.json
func TestOccurrenceRelabel(t *testing.T) {
	if *relabelOut == "" {
		t.Skip("no -occurrence-relabel-out")
	}
	llmtest.Setup(t)
	client, err := llm.NewClientFromEnv("occurrence-relabel")
	if err != nil {
		t.Fatal(err)
	}
	pairs := loadOccurrenceEvalPairs(t)
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	type label struct {
		Name      string `json:"name"`
		OldLabel  string `json:"old_label"`
		OldSure   bool   `json:"old_sure"`
		MeetsRule bool   `json:"meets_rule"`
		OccasionA string `json:"occasion_a"`
		OccasionB string `json:"occasion_b"`
		Reasoning string `json:"reasoning"`
		Label     string `json:"label"`
		Sure      bool   `json:"sure"`
		Error     string `json:"error,omitempty"`
	}
	labels := make([]label, len(pairs))
	llmtest.ForEach(len(pairs), func(i int) {
		p := pairs[i]
		l := &labels[i]
		l.Name, l.OldLabel, l.OldSure, l.MeetsRule = p.Name, p.Label, p.Sure, p.meetsRule()
		x, y := p.evidence(t)
		prompt := strings.TrimSuffix(buildOccurrencePrompt(x, y), occurrenceFormat) + occurrenceLabelFormat
		content, _, err := client.Chat(ctx, llm.ChatRequest{
			Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
			MaxTokens:      1024,
			ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
			Task:           llm.TaskOccurrence,
		})
		var reply label
		if err == nil {
			reply, err = llm.ParseJSONResponse[label](content)
		}
		if err != nil {
			l.Error = err.Error()
			return
		}
		l.OccasionA, l.OccasionB, l.Reasoning, l.Label, l.Sure = reply.OccasionA, reply.OccasionB, reply.Reasoning, reply.Label, reply.Sure
	})

	var changed, failed int
	for _, l := range labels {
		switch {
		case l.Error != "":
			failed++
			t.Errorf("%s: %s", l.Name, l.Error)
		case l.Label != l.OldLabel || l.Sure != l.OldSure:
			changed++
		}
	}
	out, err := json.MarshalIndent(labels, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*relabelOut, out, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("=== OCCURRENCE RELABEL: %d pairs, %d with a new label or certainty, %d errors; written to %s ===", len(labels), changed, failed, *relabelOut)
}
