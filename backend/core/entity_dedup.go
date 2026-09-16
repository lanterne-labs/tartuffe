package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"

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
	client *llm.Client
}

// NewEntityDedupAdjudicatorFromEnv builds an adjudicator from OPEN_AI_ env vars.
func NewEntityDedupAdjudicatorFromEnv() (*EntityDedupAdjudicator, error) {
	client, err := llm.NewClientFromEnv("entity-dedup")
	if err != nil {
		return nil, err
	}
	return &EntityDedupAdjudicator{client: client}, nil
}

// AdjudicatePair decides whether two candidate entities are the SAME real-world
// political entity. Spelling/punctuation/acronym/translation variants of one
// entity are the same; a national party vs a regional/local branch is NOT; two
// genuinely different parties are NOT.
func (a *EntityDedupAdjudicator) AdjudicatePair(ctx context.Context, b1, b2 EntityRef) (*PairVerdict, error) {
	prompt := a.buildPrompt(b1, b2)

	content, _, err := a.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		MaxTokens:      256,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return nil, fmt.Errorf("entity-dedup LLM call failed: %w", err)
	}

	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse entity-dedup response")
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return &verdict, nil
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
- NOT same: parties from DIFFERENT countries/regions, even if the name matches.
  e.g. "UK Labour"/"the labour party" vs "Australian Labor Party"; "Les Patriotes" (France) vs "PASTEF – Les Patriotes" (Senegal). When in doubt about country, answer NOT same.
- NOT same: a bare generic descriptor ("labour", "the greens", "socialists", "conservatives") vs a specific named national party — too vague to confirm as the same entity.

PAIR:
%s

Return ONLY strict JSON, nothing else:
{"same": true, "reasoning": "<one short sentence>"}
or
{"same": false, "reasoning": "<one short sentence>"}`, string(pairJSON))
}
