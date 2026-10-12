package core

import (
	"context"
	"digital-mirror/backend/llm"
	_ "embed"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// FactDedupeAdjudicator is the grouping gate: it asks an LLM whether two
// facts of one person report one occurrence (see claim_evidence.go).
type FactDedupeAdjudicator struct {
	client *llm.Client
}

// NewFactDedupeAdjudicatorFromEnv builds an adjudicator from OPEN_AI_ env vars.
func NewFactDedupeAdjudicatorFromEnv() (*FactDedupeAdjudicator, error) {
	client, err := llm.NewClientFromEnv("fact-dedupe")
	if err != nil {
		return nil, err
	}
	return &FactDedupeAdjudicator{client: client}, nil
}

// IsSameOccurrence is the grouping gate: do the two facts report ONE
// occurrence (see claim_evidence.go)? The gate reads each fact with its source
// quotes, publication dates and outlets; two one-sentence summaries alone do
// not tell a second report of a statement from another statement on the same
// topic.
//
// The verdict must hold in both orders: the gate reads the second statement
// against the first, and a pair of different claims often reads as one
// occurrence one way only. The second call is made only when the first says
// groupable. "Same" needs both orders to say same; a "bundle" answer in
// either order makes the pair contained.
func (a *FactDedupeAdjudicator) IsSameOccurrence(ctx context.Context, x, y ClaimEvidence) (OccurrenceVerdict, error) {
	first, err := a.askOccurrenceGate(ctx, x, y)
	if err != nil || !first.Groupable() {
		return first, err
	}
	second, err := a.askOccurrenceGate(ctx, y, x)
	if err != nil || !second.Groupable() {
		return second, err
	}
	if first == OccurrenceSame && second == OccurrenceSame {
		return OccurrenceSame, nil
	}
	return OccurrenceContained, nil
}

//go:embed occurrence_rules.txt
var occurrenceRules string

const occurrenceFormat = `
Return ONLY strict JSON, nothing else, with the keys in this order:
{"occasion_a": "<what the person said or did in A, and when/where, from the evidence>", "occasion_b": "<the same for B>", "reasoning": "<one sentence: one occurrence, or not, and the deciding evidence>", "verdict": "same" | "different" | "bundle"}`

// buildOccurrencePrompt lays the prompt out rules first, the two statements
// next, response format last: the rule block is byte-identical across calls,
// so vLLM prefix caching prefills it once per process instead of once per
// candidate pair (see CHANGES.md, prompt layout).
func buildOccurrencePrompt(x, y ClaimEvidence) string {
	var b strings.Builder
	b.WriteString(occurrenceRules)
	b.WriteString("\n\n")
	writeClaimEvidence(&b, "A", x)
	b.WriteString("\n\n")
	writeClaimEvidence(&b, "B", y)
	b.WriteString("\n")
	b.WriteString(occurrenceFormat)
	return b.String()
}

func writeClaimEvidence(b *strings.Builder, name string, e ClaimEvidence) {
	fmt.Fprintf(b, "Statement %s: %s", name, e.Text)
	for _, s := range e.Sources {
		date := "unknown date"
		if s.PublishedAt != nil {
			date = s.PublishedAt.Format("2006-01-02")
		}
		fmt.Fprintf(b, "\n  source: %s, published %s, quote: \"%s\"", s.Outlet, date, strings.ReplaceAll(s.Quote, "\n", " "))
	}
}

type occurrenceReply struct {
	OccasionA string `json:"occasion_a"`
	OccasionB string `json:"occasion_b"`
	Reasoning string `json:"reasoning"`
	Verdict   string `json:"verdict"`
}

// askOccurrenceGate is TaskOccurrence, which thinks in production (task.go).
func (a *FactDedupeAdjudicator) askOccurrenceGate(ctx context.Context, x, y ClaimEvidence) (OccurrenceVerdict, error) {
	content, _, err := a.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: buildOccurrencePrompt(x, y)}},
		Temperature:    0.0,
		MaxTokens:      512,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskOccurrence,
	})
	if err != nil {
		return OccurrenceDifferent, fmt.Errorf("occurrence-gate LLM call failed: %w", err)
	}
	reply, err := llm.ParseJSONResponse[occurrenceReply](content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse occurrence-gate response")
		return OccurrenceDifferent, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	log.Debug().Str("occasion_a", reply.OccasionA).Str("occasion_b", reply.OccasionB).
		Str("reasoning", reply.Reasoning).Str("verdict", reply.Verdict).Msg("occurrence gate")
	switch reply.Verdict {
	case "same":
		return OccurrenceSame, nil
	case "bundle":
		return OccurrenceContained, nil
	case "different":
		return OccurrenceDifferent, nil
	default:
		return OccurrenceDifferent, fmt.Errorf("occurrence gate: unknown verdict %q", reply.Verdict)
	}
}
