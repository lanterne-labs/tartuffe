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

// DedupLLMVerifier uses an LLM to verify whether facts are duplicates.
type DedupLLMVerifier struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// LLMDedupeResponse is the expected JSON response from the LLM.
type LLMDedupeResponse struct {
	UniqueGroups     []UniqueGroup     `json:"unique_groups"`
	FactImprovements []FactImprovement `json:"fact_improvements"`
	InvalidFacts     []InvalidFact     `json:"invalid_facts"`
}

// UniqueGroup represents a group of facts where one is kept and others are duplicates.
type UniqueGroup struct {
	KeepID       int64   `json:"keep_id"`
	DuplicateIDs []int64 `json:"duplicate_ids"`
	Reason       string  `json:"reason"`
}

// FactImprovement represents a suggested improvement to a fact's text.
type FactImprovement struct {
	ID                     int64  `json:"id"`
	ImprovedNormalizedFact string `json:"improved_normalized_fact"`
	ImprovedEmbeddingText  string `json:"improved_embedding_text"`
	Reason                 string `json:"reason"`
}

// InvalidFact represents a fact that should be deleted (not a real fact about the person).
type InvalidFact struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// NewDedupLLMVerifierFromEnv creates a verifier using OPEN_AI_ environment variables.
func NewDedupLLMVerifierFromEnv() (*DedupLLMVerifier, error) {
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

	baseClient := llm.NewBaseLLMClient()

	// Build chat completions URL
	base = strings.TrimRight(base, "/")
	apiURL := base + "/chat/completions"

	return &DedupLLMVerifier{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: baseClient,
	}, nil
}

// VerifyBatch sends a batch of facts to the LLM for deduplication verification.
// Returns groups of duplicate facts with reasoning.
func (v *DedupLLMVerifier) VerifyBatch(ctx context.Context, facts []FactForDedup) (*LLMDedupeResponse, error) {
	if len(facts) < 2 {
		return &LLMDedupeResponse{}, nil
	}

	// Build the prompt
	prompt := v.buildPrompt(facts)

	// Build Chat Request
	reqPayload := llm.ChatRequest{
		Model: v.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal dedup request payload: %w", err)
	}

	log.Debug().Int("fact_count", len(facts)).Msg("Sending facts to LLM for deduplication verification")

	// Call the LLM
	bodyBytes, err := v.baseClient.DoRequest(ctx, v.apiURL, v.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM dedup verification failed: %w", err)
	}

	// Parse the response
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode chat response")
		return nil, fmt.Errorf("failed to decode chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("LLM returned no content")
		return nil, fmt.Errorf("LLM returned no content")
	}

	// Parse the JSON response from LLM
	llmContent := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	response, err := v.parseResponse(llmContent)
	if err != nil {
		log.Error().Err(err).Str("llm_content", llmContent).Msg("Failed to parse LLM dedup response")
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
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
		EmbeddingText  string `json:"embedding_text"`
		OriginalQuote  string `json:"original_quote,omitempty"`
	}

	factsForPrompt := make([]factJSON, len(facts))
	for i, f := range facts {
		factsForPrompt[i] = factJSON{
			ID:             f.ID,
			NormalizedFact: f.NormalizedFactText,
			EmbeddingText:  f.EmbeddingText,
			OriginalQuote:  f.OriginalQuote,
		}
	}

	factsJSON, _ := json.MarshalIndent(factsForPrompt, "", "  ")

	prompt := fmt.Sprintf(`You are a fact quality and deduplication assistant. Analyze these facts and:

1. IDENTIFY DUPLICATES - facts that say the same thing
2. FLAG INVALID FACTS - things that are NOT facts about the person
3. SUGGEST IMPROVEMENTS - make facts more precise/distinct

## WHAT IS A VALID FACT?
✅ Type 1 — Entity's own positions, promises, claims, denials:
   - "X stated she had no feeling of committing a crime"
   - "X denied the allegations against her"
   - "X claimed the Parliament did not play its role"

✅ Type 2 — Authoritative verifiable outcomes:
   - "X was sentenced to 4 years in prison" — court ruling
   - "X was found guilty of misappropriation" — judicial finding

❌ INVALID — Third-party opinions without evidence:
   - "Critics say X is corrupt" — not X's action or authoritative finding

❌ INVALID — Non-contradictable information:
   - "X attended the hearing" — scheduling
   - "X was born in 1968" — biography

## DUPLICATES
DUPLICATE means:
- Same statement in different languages
- Same quote with minor wording differences
- One fact is a subset of another

NOT duplicates: Same person, different actions/dates/claims

## IMPROVEMENTS
Suggest improvements when:
- A fact could be more specific about WHAT the person did/said
- The embedding_text is too generic and could match unrelated facts
- The fact mixes action with opinion (split or refocus on action)

INPUT FACTS:
%s

Return JSON in exactly this format:
{
  "unique_groups": [
    {"keep_id": 1, "duplicate_ids": [2], "reason": "Same statement in FR/EN"}
  ],
  "invalid_facts": [
    {"id": 3, "reason": "Opinion about Parliament, not a fact about the person's actions"}
  ],
  "fact_improvements": [
    {
      "id": 4,
      "improved_normalized_fact": "Marine Le Pen stated during her appeal trial that she had no feeling of committing a crime when hiring EU parliamentary assistants.",
      "improved_embedding_text": "DENIED wrongdoing parliamentary-assistants-case",
      "reason": "Made more specific about what she claimed"
    }
  ]
}

Rules:
- Only include arrays that have items (omit empty arrays)
- A fact can appear in only ONE of: unique_groups, invalid_facts, or fact_improvements
- If a fact is fine as-is, don't include it anywhere
- Return {"unique_groups": [], "invalid_facts": [], "fact_improvements": []} if everything is fine

IMPORTANT: Return ONLY valid JSON.`, string(factsJSON))

	return prompt
}

// parseResponse parses the LLM's JSON response into a structured format.
func (v *DedupLLMVerifier) parseResponse(content string) (*LLMDedupeResponse, error) {
	// Try to extract JSON if wrapped in markdown code blocks
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

	var response LLMDedupeResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}

	// Validate the response
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
