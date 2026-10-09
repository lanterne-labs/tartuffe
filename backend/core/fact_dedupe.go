package core

import (
	"context"
	"digital-mirror/backend/llm"
	_ "embed"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// FactDedupeAdjudicator asks an LLM whether two same-subject statements express
// the SAME claim (one a redundant restatement of the other) or are genuinely
// distinct. It is consulted only for the "gray zone" of embedding similarity
// (below the auto-dedup threshold but above the clearly-distinct floor), where
// cosine similarity alone cannot decide and the verb heuristic is too coarse —
// e.g. "il ne renoncera pas à sa candidature et ira jusqu'au bout" (PROMISED)
// vs "sa candidature irait jusqu'au bout" (CLAIMED): the same claim in different
// words, similarity 0.78, which slips just under the 0.80 auto-dedup line.
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

// IsSameClaim returns true when factB is a redundant restatement of factA by the
// same person — i.e. they should be deduplicated. Distinct claims, opposing
// stances, and different specifics return false. The subject name is passed for
// context (both facts are about the same person).
func (a *FactDedupeAdjudicator) IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error) {
	prompt := a.buildPrompt(subject, factA, factB)
	return a.askSameGate(ctx, prompt, 0.1)
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
// next, response format last (see the layout note below).
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
	Verdict string `json:"verdict"`
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

// The gate prompts are laid out rules first, per-call data (person, the two
// statements) next, response format last: the rule block is byte-identical
// across calls, so vLLM prefix caching prefills it once per process instead
// of once per candidate pair (see CHANGES.md, prompt layout).

// askSameGate runs a same/distinct JSON gate prompt at the given temperature
// and parses the verdict (the PairVerdict same/reasoning shape defined in
// entity_dedup.go).
func (a *FactDedupeAdjudicator) askSameGate(ctx context.Context, prompt string, temperature float64) (bool, error) {
	content, _, err := a.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    temperature,
		MaxTokens:      512,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskSameClaim,
	})
	if err != nil {
		return false, fmt.Errorf("same-gate LLM call failed: %w", err)
	}
	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse fact-dedupe response")
		return false, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return verdict.Same, nil
}

func (a *FactDedupeAdjudicator) buildPrompt(subject, factA, factB string) string {
	return fmt.Sprintf(`Two statements are recorded about the SAME person. Decide whether they are the SAME claim — one a redundant restatement of the other carrying NO new information — so that keeping both would be pure duplication.

This is a deduplication gate, so be CONSERVATIVE: when in doubt, answer false. Wrongly merging two statements deletes real information; keeping a redundant one is harmless. Only answer true when B is essentially A reworded.

SAME claim (answer true): the same assertion in different words — different reporting verb ("promised"/"claimed"/"declared"/"stated"/"affirmed" are interchangeable) or extra incidental wording, but the SAME subject, SAME direction, and SAME substance. e.g. "X will not give up his candidacy and will see it through" vs "X's candidacy would go all the way".

DISTINCT claims (answer false — keep both):
- The two could be presented as a CONTRADICTION or a change of position: a promise/commitment/stated value vs a later or opposing ACTION (e.g. "promised to keep the retirement age at 62" vs "abandoned the repeal of the move to 64"); a before vs after; "supports X" vs "opposes X". These are NEVER the same — merging them would erase a real contradiction.
- Different specifics: different policies, dates, people, places, numbers, or scope.
- A general position vs a specific concrete act that advances it.
- Any case where B states something A does not, or vice versa.

Person: %s
STATEMENT A: "%s"
STATEMENT B: "%s"

Return ONLY strict JSON, nothing else:
{"reasoning": "<one short sentence>", "same": true}
or
{"reasoning": "<one short sentence>", "same": false}`, subject, factA, factB)
}
