package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"os"
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
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewFactDedupeAdjudicatorFromEnv builds an adjudicator from OPEN_AI_ env vars.
func NewFactDedupeAdjudicatorFromEnv() (*FactDedupeAdjudicator, error) {
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}

	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}

	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	base = strings.TrimRight(base, "/")
	return &FactDedupeAdjudicator{
		apiKey:     apiKey,
		apiURL:     llm.ChatCompletionsURL(base),
		model:      model,
		baseClient: llm.NewBaseLLMClient("fact-dedupe"),
	}, nil
}

// IsSameClaim returns true when factB is a redundant restatement of factA by the
// same person — i.e. they should be deduplicated. Distinct claims, opposing
// stances, and different specifics return false. The subject name is passed for
// context (both facts are about the same person).
func (a *FactDedupeAdjudicator) IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error) {
	prompt := a.buildPrompt(subject, factA, factB)

	reqPayload := llm.ChatRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		MaxTokens:      256,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal fact-dedupe request: %w", err)
	}

	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return false, fmt.Errorf("fact-dedupe LLM call failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode fact-dedupe chat response")
		return false, fmt.Errorf("failed to decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return false, fmt.Errorf("LLM returned no content")
	}

	verdict, err := parseSameClaimResponse(strings.TrimSpace(chatResp.Choices[0].Message.Content))
	if err != nil {
		log.Error().Err(err).Str("llm_content", chatResp.Choices[0].Message.Content).Msg("Failed to parse fact-dedupe response")
		return false, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return verdict.Same, nil
}

// IsSamePosition is the meta-FOLDING gate: unlike IsSameClaim (which guards a
// destructive drop and therefore treats any differing specific as distinct),
// folding keeps every raw fact, so this gate asks the broader question — do the
// two statements assert the SAME POSITION on the SAME specific matter, differing
// only in wording, emphasis, or facet? "Affirms his innocence", "claims good
// faith", and "contests the qualification of embezzlement" about the same affair
// all fold; a commitment vs a contrary action, opposing positions, or different
// matters never do (those pairs are contradiction material and must stay apart).
func (a *FactDedupeAdjudicator) IsSamePosition(ctx context.Context, subject, factA, factB string) (bool, error) {
	prompt := a.buildSamePositionPrompt(subject, factA, factB)
	return a.askSameGate(ctx, prompt)
}

func (a *FactDedupeAdjudicator) buildSamePositionPrompt(subject, factA, factB string) string {
	return fmt.Sprintf(`Two statements are recorded about the SAME person (%s). Decide whether they assert the SAME POSITION on the SAME specific matter — so that they can be AGGREGATED under one summary statement. The originals are kept either way, so lean toward true when the position and the matter are the same.

SAME position (answer true):
- The same stance on the same question, in different words or through different facets: "affirms his innocence in the affair", "claims his good faith", "contests the qualification of embezzlement" — all one position ("denies guilt in that affair").
- The same announcement/position reported with different incidental detail (outlet, phrasing, minor date differences, added color).
- The same demand/support/opposition restated over time with the same direction and target.

DIFFERENT (answer false — never aggregate):
- OPPOSING or diverging positions, or a position vs a later change: these are contradiction material and must stay separate.
- A commitment/promise/value vs an ACTION or outcome (even one that fulfils it): a statement and a deed are different kinds of facts.
- Different matters, targets, or questions (two different affairs, laws, elections, policies) — even if thematically close.
- Different concrete events (two separate votes, two separate trials/instances, two separate polls) or materially different numbers/sanctions as the substance of the statement.
- Different PROCEDURAL STAGES of the same affair: a prosecutor's request (réquisition) vs the court's decision, an indictment vs a verdict, a first-instance ruling vs the appeal — each is its own fact.

STATEMENT A: "%s"
STATEMENT B: "%s"

Return ONLY strict JSON, nothing else:
{"same": true, "reasoning": "<one short sentence>"}
or
{"same": false, "reasoning": "<one short sentence>"}`, subject, factA, factB)
}

// askSameGate runs a same/distinct JSON gate prompt and parses the verdict.
func (a *FactDedupeAdjudicator) askSameGate(ctx context.Context, prompt string) (bool, error) {
	reqPayload := llm.ChatRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    0.0,
		MaxTokens:      256,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal same-gate request: %w", err)
	}
	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return false, fmt.Errorf("same-gate LLM call failed: %w", err)
	}
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return false, fmt.Errorf("failed to decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return false, fmt.Errorf("LLM returned no content")
	}
	verdict, err := parseSameClaimResponse(strings.TrimSpace(chatResp.Choices[0].Message.Content))
	if err != nil {
		return false, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return verdict.Same, nil
}

func (a *FactDedupeAdjudicator) buildPrompt(subject, factA, factB string) string {
	return fmt.Sprintf(`Two statements are recorded about the SAME person (%s). Decide whether they are the SAME claim — one a redundant restatement of the other carrying NO new information — so that keeping both would be pure duplication.

This is a deduplication gate, so be CONSERVATIVE: when in doubt, answer false. Wrongly merging two statements deletes real information; keeping a redundant one is harmless. Only answer true when B is essentially A reworded.

SAME claim (answer true): the same assertion in different words — different reporting verb ("promised"/"claimed"/"declared"/"stated"/"affirmed" are interchangeable) or extra incidental wording, but the SAME subject, SAME direction, and SAME substance. e.g. "X will not give up his candidacy and will see it through" vs "X's candidacy would go all the way".

DISTINCT claims (answer false — keep both):
- The two could be presented as a CONTRADICTION or a change of position: a promise/commitment/stated value vs a later or opposing ACTION (e.g. "promised to keep the retirement age at 62" vs "abandoned the repeal of the move to 64"); a before vs after; "supports X" vs "opposes X". These are NEVER the same — merging them would erase a real contradiction.
- Different specifics: different policies, dates, people, places, numbers, or scope.
- A general position vs a specific concrete act that advances it.
- Any case where B states something A does not, or vice versa.

STATEMENT A: "%s"
STATEMENT B: "%s"

Return ONLY strict JSON, nothing else:
{"same": true, "reasoning": "<one short sentence>"}
or
{"same": false, "reasoning": "<one short sentence>"}`, subject, factA, factB)
}

// parseSameClaimResponse tolerates fenced/unfenced JSON, reusing the PairVerdict
// shape (same/reasoning) defined in entity_dedup.go.
func parseSameClaimResponse(content string) (*PairVerdict, error) {
	content = llm.StripJSONCodeFence(content)

	var v PairVerdict
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return &v, nil
}
