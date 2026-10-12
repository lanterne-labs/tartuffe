package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// DedupLLMVerifier uses an LLM to verify whether facts are duplicates.
type DedupLLMVerifier struct {
	client *llm.Client
}

// LLMDedupeAnalysisEntry represents a pairwise reasoning entry from the LLM.
type LLMDedupeAnalysisEntry struct {
	IDs       []int64 `json:"ids"`
	Reasoning string  `json:"reasoning"`
}

// LLMDedupeResponse is the expected JSON response from the LLM.
type LLMDedupeResponse struct {
	Analysis     []LLMDedupeAnalysisEntry `json:"analysis"` // Pairwise reasoning (logged, not acted on)
	UniqueGroups []UniqueGroup            `json:"unique_groups"`
	InvalidFacts []InvalidFact            `json:"invalid_facts"`
}

// UniqueGroup represents a group of facts where one is kept and others are duplicates.
type UniqueGroup struct {
	KeepID       int64   `json:"keep_id"`
	DuplicateIDs []int64 `json:"duplicate_ids"`
	Reason       string  `json:"reason"`
}

// InvalidFact represents a fact that should be deleted (not a real fact about the person).
type InvalidFact struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// NewDedupLLMVerifierFromEnv creates a verifier using OPEN_AI_ environment variables.
func NewDedupLLMVerifierFromEnv() (*DedupLLMVerifier, error) {
	client, err := llm.NewClientFromEnv("dedup-filter")
	if err != nil {
		return nil, err
	}
	return &DedupLLMVerifier{client: client}, nil
}

// VerifyBatch sends a batch of facts to the LLM for deduplication verification.
// Returns groups of duplicate facts with reasoning.
func (v *DedupLLMVerifier) VerifyBatch(ctx context.Context, facts []FactForDedup) (*LLMDedupeResponse, error) {
	if len(facts) < 2 {
		return &LLMDedupeResponse{}, nil
	}

	prompt := v.buildPrompt(facts)

	log.Debug().Int("fact_count", len(facts)).Msg("Sending facts to LLM for deduplication verification")

	llmContent, _, err := v.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		// Reply is a short JSON dedupe decision; 16384 exceeded the local
		// model's entire 12k context and was rejected with 400 up front.
		MaxTokens:      2048,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskDedupBatch,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM dedup verification failed: %w", err)
	}

	llmContent = strings.TrimSpace(llmContent)
	response, err := parseDedupeResponse(llmContent)
	if err != nil {
		log.Error().Err(err).Str("llm_content", llmContent).Msg("Failed to parse LLM dedup response")
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Log pairwise analysis entries at debug level
	for _, a := range response.Analysis {
		log.Debug().
			Interface("ids", a.IDs).
			Str("reasoning", a.Reasoning).
			Msg("LLM dedup pairwise analysis")
	}

	log.Info().Int("duplicate_groups", len(response.UniqueGroups)).Msg("LLM deduplication verification complete")

	return response, nil
}

// buildPrompt constructs the LLM prompt for deduplication verification.
func (v *DedupLLMVerifier) buildPrompt(facts []FactForDedup) string {
	// Build JSON array of facts for the prompt
	type factJSON struct {
		ID             int64  `json:"id"`
		NormalizedFact string `json:"normalized_fact"`
		CanonicalVerb  string `json:"canonical_verb,omitempty"`
		EmbeddingText  string `json:"embedding_text"`
		OriginalQuote  string `json:"original_quote,omitempty"`
	}

	factsForPrompt := make([]factJSON, len(facts))
	for i, f := range facts {
		factsForPrompt[i] = factJSON{
			ID:             f.ID,
			NormalizedFact: f.NormalizedFactText,
			CanonicalVerb:  f.CanonicalVerb,
			EmbeddingText:  f.EmbeddingText,
			OriginalQuote:  f.OriginalQuote,
		}
	}

	factsJSON, _ := json.MarshalIndent(factsForPrompt, "", "  ")

	prompt := fmt.Sprintf(`You are a fact quality and deduplication assistant. Each fact has a canonical_verb (the stance) and embedding_text (the topic).

## STEP 1: Pairwise analysis
For each potential duplicate pair, write a short justification of your decision for a human reviewer: same topic AND same stance? Or different actions/dates?

## STEP 2: Based on the analysis, produce the output

### WHAT IS A VALID FACT?
✅ Type 1 — Entity's own positions, promises, claims, denials
✅ Type 2 — Authoritative verifiable outcomes (court rulings, votes, documented findings)
❌ INVALID — Third-party opinions, non-contradictable info (scheduling, biography)

### DUPLICATES
DUPLICATE means: same statement in different languages, minor wording differences, or one is a subset of another.
NOT duplicates: same person but different actions/dates/claims. CRITICAL: Two facts with DIFFERENT canonical_verbs are almost NEVER duplicates (e.g., DENIED vs WAS_CONVICTED are opposing stances on the same topic).

INPUT FACTS:
%s

Return JSON:
{
  "analysis": [
    {"ids": [1, 2], "reasoning": "Same verb DENIED + same topic wrongdoing → duplicate"}
  ],
  "unique_groups": [
    {"reason": "Same denial in FR/EN", "keep_id": 1, "duplicate_ids": [2]}
  ],
  "invalid_facts": [
    {"reason": "Opinion, not a verifiable fact", "id": 3}
  ]
}

Rules:
- analysis is the justification of each pair decision for the reviewer — it does not affect the output structure
- A fact can appear in only ONE of: unique_groups or invalid_facts
- Return {"unique_groups": [], "invalid_facts": []} if everything is fine

IMPORTANT: Return ONLY valid JSON.`, string(factsJSON))

	return prompt
}

// parseDedupeResponse parses the LLM's JSON response and drops zero-valued
// ids from every group.
func parseDedupeResponse(content string) (*LLMDedupeResponse, error) {
	response, err := llm.ParseJSONResponse[LLMDedupeResponse](content)
	if err != nil {
		return nil, err
	}

	for i, group := range response.UniqueGroups {
		if group.KeepID == 0 {
			log.Warn().Int("group_index", i).Msg("LLM returned group with zero keep_id, skipping")
			continue
		}
		// Filter out zero-valued duplicate IDs
		validDuplicates := make([]int64, 0, len(group.DuplicateIDs))
		for _, id := range group.DuplicateIDs {
			if id != 0 && id != group.KeepID {
				validDuplicates = append(validDuplicates, id)
			}
		}
		response.UniqueGroups[i].DuplicateIDs = validDuplicates
	}

	return &response, nil
}
