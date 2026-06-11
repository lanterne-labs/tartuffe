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
