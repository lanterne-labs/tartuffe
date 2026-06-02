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

// EntityRef is a candidate person handed to the dedup adjudicator.
type EntityRef struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	FactCount  int    `json:"fact_count"`
}

// PairVerdict is the LLM's decision for one candidate pair.
type PairVerdict struct {
	Same      bool   `json:"same"`
	Reasoning string `json:"reasoning"`
}

// EntityDedupAdjudicator asks an LLM whether two similarly-named persons refer to
// the same real-world entity.
type EntityDedupAdjudicator struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewEntityDedupAdjudicatorFromEnv builds an adjudicator from OPEN_AI_ env vars.
func NewEntityDedupAdjudicatorFromEnv() (*EntityDedupAdjudicator, error) {
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
	return &EntityDedupAdjudicator{
		apiKey:     apiKey,
		apiURL:     base + "/chat/completions",
		model:      model,
		baseClient: llm.NewBaseLLMClient("entity-dedup"),
	}, nil
}

// AdjudicatePair decides whether two candidate entities are the SAME real-world
// political entity. Spelling/punctuation/acronym/translation variants of one
// entity are the same; a national party vs a regional/local branch is NOT; two
// genuinely different parties are NOT.
func (a *EntityDedupAdjudicator) AdjudicatePair(ctx context.Context, b1, b2 EntityRef) (*PairVerdict, error) {
	prompt := a.buildPrompt(b1, b2)

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
		return nil, fmt.Errorf("failed to marshal entity-dedup request: %w", err)
	}

	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("entity-dedup LLM call failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode entity-dedup chat response")
		return nil, fmt.Errorf("failed to decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("entity-dedup LLM returned no content")
		return nil, fmt.Errorf("LLM returned no content")
	}

	verdict, err := a.parseResponse(strings.TrimSpace(chatResp.Choices[0].Message.Content))
	if err != nil {
		log.Error().Err(err).Str("llm_content", chatResp.Choices[0].Message.Content).Msg("Failed to parse entity-dedup response")
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return verdict, nil
}

// ClusterPairs groups ids into connected components given a list of unordered
// id pairs (union-find). Every id that appears in `pairs` ends up in exactly one
// component; singletons (ids not in any pair) are not represented. The returned
// clusters each have >= 2 members. Order within and across clusters is not
// guaranteed.
func ClusterPairs(pairs [][2]int64) [][]int64 {
	parent := make(map[int64]int64)
	var find func(x int64) int64
	find = func(x int64) int64 {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int64) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, p := range pairs {
		union(p[0], p[1])
	}

	groups := make(map[int64][]int64)
	for id := range parent {
		root := find(id)
		groups[root] = append(groups[root], id)
	}

	clusters := make([][]int64, 0, len(groups))
	for _, members := range groups {
		if len(members) >= 2 {
			clusters = append(clusters, members)
		}
	}
	return clusters
}

func (a *EntityDedupAdjudicator) buildPrompt(b1, b2 EntityRef) string {
	pairJSON, _ := json.MarshalIndent([]EntityRef{b1, b2}, "", "  ")

	return fmt.Sprintf(`You decide whether TWO candidate entities are the SAME real-world political entity (a person, party, or organization). They have similar names but may be variants of one entity OR genuinely different entities that merely look alike.

RULES:
- SAME entity: spelling/accent/punctuation/acronym/translation variants of one entity.
  e.g. "eric zemmour" vs "éric zemmour"; "rassemblement national" vs "rn".
- NOT same: a national party vs a regional/local branch of it (e.g. "Parti Socialiste" vs a PS departmental federation).
- NOT same: two genuinely different parties/people that share a generic token ("party", "democratic", "socialiste").

PAIR:
%s

Return ONLY strict JSON, nothing else:
{"same": true, "reasoning": "<one short sentence>"}
or
{"same": false, "reasoning": "<one short sentence>"}`, string(pairJSON))
}

func (a *EntityDedupAdjudicator) parseResponse(content string) (*PairVerdict, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			content = content[:idx]
		}
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			content = content[:idx]
		}
		content = strings.TrimSpace(content)
	}

	var v PairVerdict
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return &v, nil
}
