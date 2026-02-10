package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"

	"github.com/rs/zerolog/log"
)

// RelationClassifier uses an LLM to classify the relationship between a new fact
// and existing similar facts as: duplicate, implication, or unrelated.
type RelationClassifier struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewRelationClassifierFromEnv creates a RelationClassifier using OPEN_AI_ env vars.
func NewRelationClassifierFromEnv() (*RelationClassifier, error) {
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

	base = strings.TrimRight(base, "/")
	apiURL := base
	if !strings.HasSuffix(base, "/chat/completions") {
		apiURL = base + "/chat/completions"
	}

	return &RelationClassifier{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: baseClient,
	}, nil
}

// ClassificationInput represents a similar fact to classify against the new fact.
type ClassificationInput struct {
	ExistingFactID   int64
	ExistingFactText string
	PublishedAt      *time.Time
}

// ClassificationResult represents the classification of one existing fact relative to the new fact.
type ClassificationResult struct {
	ExistingFactID int64
	Relation       string // "duplicate", "implication_new_is_general", "implication_new_is_specific", "unrelated"
	Reason         string
}

// llmClassificationResponse is the expected JSON response from the LLM.
type llmClassificationResponse struct {
	Classifications []llmClassification `json:"classifications"`
}

type llmClassification struct {
	ExistingFactID int64  `json:"existing_fact_id"`
	Relation       string `json:"relation"`
	Reason         string `json:"reason"`
}

const classificationPrompt = `You are analyzing how a NEW fact relates to EXISTING facts about the same person/entity.

For each existing fact, classify its relationship to the new fact as one of:
- "duplicate": The new fact and existing fact express the same information (possibly with different wording).
- "implication_new_is_general": The new fact is MORE GENERAL - the existing fact is a specific instance or subset of the new fact. (Example: new="X voted against environmental bills" is general; existing="X voted against Bill 123 on carbon tax" is specific.)
- "implication_new_is_specific": The new fact is MORE SPECIFIC - it's a specific instance of the existing fact.
- "unrelated": The facts are about different topics or have no logical subsumption relationship.

**IMPORTANT RULES:**
- "duplicate" means they say essentially the same thing, not just that they're related
- An implication means one logically ENTAILS the other (knowing A guarantees B)
- Voting on the same general topic is NOT an implication unless one statement genuinely subsumes the other
- When in doubt, classify as "unrelated" - false positives are worse than misses

NEW FACT:
Text: %s
Published: %s

EXISTING FACTS:
%s

Respond ONLY with a JSON object in this exact format:
{"classifications": [{"existing_fact_id": <id>, "relation": "<type>", "reason": "<brief explanation>"}]}
`

// Classify classifies the relationship between a new fact and a set of similar existing facts.
// Returns classification results for each existing fact.
func (rc *RelationClassifier) Classify(ctx context.Context, newFactText string, newFactPublishedAt *time.Time, existingFacts []ClassificationInput) ([]ClassificationResult, error) {
	if len(existingFacts) == 0 {
		return nil, nil
	}

	timer := metrics.NewLLMTimer("chat", rc.model)

	// Format existing facts for the prompt
	var factsSection strings.Builder
	for _, f := range existingFacts {
		pubDate := "unknown date"
		if f.PublishedAt != nil {
			pubDate = f.PublishedAt.Format("January 2, 2006")
		}
		fmt.Fprintf(&factsSection, "- ID: %d | Text: %s | Published: %s\n", f.ExistingFactID, f.ExistingFactText, pubDate)
	}

	newPubDate := "unknown date"
	if newFactPublishedAt != nil {
		newPubDate = newFactPublishedAt.Format("January 2, 2006")
	}

	prompt := fmt.Sprintf(classificationPrompt, newFactText, newPubDate, factsSection.String())

	// Build request
	reqBody := llm.ChatRequest{
		Model: rc.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    0.0,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal classification request: %w", err)
	}

	respBytes, err := rc.baseClient.DoRequest(ctx, rc.apiURL, rc.apiKey, payloadBytes)
	timer.ObserveDuration(err)
	if err != nil {
		return nil, fmt.Errorf("classification LLM call failed: %w", err)
	}

	// Parse response
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("LLM API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty LLM response")
	}

	content := chatResp.Choices[0].Message.Content

	var classResp llmClassificationResponse
	if err := json.Unmarshal([]byte(content), &classResp); err != nil {
		log.Warn().Str("content", content).Err(err).Msg("Failed to parse classification JSON from LLM")
		return nil, fmt.Errorf("failed to parse classification response JSON: %w", err)
	}

	// Convert to results, filtering invalid relations
	validRelations := map[string]bool{
		"duplicate":                     true,
		"implication_new_is_general":    true,
		"implication_new_is_specific":   true,
		"unrelated":                     true,
	}

	var results []ClassificationResult
	for _, c := range classResp.Classifications {
		if !validRelations[c.Relation] {
			log.Warn().
				Int64("existing_fact_id", c.ExistingFactID).
				Str("relation", c.Relation).
				Msg("Invalid relation type from LLM, treating as unrelated")
			continue
		}
		if c.Relation == "unrelated" {
			continue // No relation to record
		}
		results = append(results, ClassificationResult{
			ExistingFactID: c.ExistingFactID,
			Relation:       c.Relation,
			Reason:         c.Reason,
		})
	}

	log.Info().
		Int("total_classified", len(classResp.Classifications)).
		Int("relations_found", len(results)).
		Msg("Relation classification complete")

	return results, nil
}
