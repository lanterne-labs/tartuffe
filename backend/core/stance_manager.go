package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// StanceStorer defines the storage interface for stance operations.
type StanceStorer interface {
	FindMatchingStance(ctx context.Context, personID int64, embedding []float32, threshold float64) (*StanceMatch, error)
	InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error)
	UpdateStanceDocument(ctx context.Context, id int64, summary string, embedding []float32, keyFactIDs []int64, factCount int) error
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error)
	DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error
	RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason string) error
}

// StanceDocument is the core type for stance documents.
type StanceDocument struct {
	ID               int64
	PersonID         int64
	ClusterLabel     string
	ClusterEmbedding []float32
	Summary          string
	KeyFactIDs       []int64
	FactCount        int
}

// StanceMatch is a stance found by embedding search.
type StanceMatch struct {
	StanceDocument
	Distance float64
}

// StanceFactInfo represents a fact within a stance.
type StanceFactInfo struct {
	ID                 int64
	NormalizedFactText string
	EmbeddingText      string
	CanonicalVerb      string
}

// StanceProcessResult holds the outcome of processing a new fact against existing stances.
type StanceProcessResult struct {
	Verdict            string // "NEW_STANCE", "DUPLICATE", "NEW_INFO", "CONTRADICTION"
	StanceID           int64
	Reasoning          string
	ContradictedFactID int64 // Only set for CONTRADICTION
}

// stanceLLMResponse is the expected JSON response from the stance analysis LLM call.
type stanceLLMResponse struct {
	Verdict             string `json:"verdict"`
	Reasoning           string `json:"reasoning"`
	UpdatedSummary      string `json:"updated_summary"`
	ContradictingFactID int64  `json:"contradicting_key_fact_id"`
}

// summaryLLMResponse is the expected JSON response from the summary generation LLM call.
type summaryLLMResponse struct {
	Summary    string  `json:"summary"`
	KeyFactIDs []int64 `json:"key_fact_ids"`
}

// StanceManager handles the core stance document logic: matching new facts to existing
// stances, creating new stances, and detecting contradictions via LLM analysis.
type StanceManager struct {
	storer    StanceStorer
	apiKey    string
	apiURL    string
	model     string
	client    *llm.BaseLLMClient
	threshold float64 // Distance threshold for matching stances (default 0.35)
}

// NewStanceManagerFromEnv creates a new StanceManager using OPEN_AI_* environment variables.
// Returns an error if required env vars (OPEN_AI_KEY, OPEN_AI_API) are missing.
func NewStanceManagerFromEnv(storer StanceStorer) (*StanceManager, error) {
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
	apiURL := base + "/chat/completions"

	threshold := 0.35
	if env := os.Getenv("STANCE_MATCH_THRESHOLD"); env != "" {
		if t, err := strconv.ParseFloat(env, 64); err == nil {
			threshold = t
			log.Info().Float64("threshold", threshold).Msg("Loaded STANCE_MATCH_THRESHOLD from environment")
		} else {
			log.Warn().Str("value", env).Err(err).Msg("Invalid STANCE_MATCH_THRESHOLD, using default 0.35")
		}
	}

	return &StanceManager{
		storer:    storer,
		apiKey:    apiKey,
		apiURL:    apiURL,
		model:     model,
		client:    llm.NewBaseLLMClient(),
		threshold: threshold,
	}, nil
}

// ProcessNewFact analyzes a new fact against existing stances for the given person.
// It either creates a new stance or updates an existing one, potentially detecting contradictions.
func (sm *StanceManager) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	match, err := sm.storer.FindMatchingStance(ctx, personID, embedding, sm.threshold)
	if err != nil {
		return nil, fmt.Errorf("finding matching stance: %w", err)
	}

	// No match: create a new stance document
	if match == nil {
		label := factText
		if len(label) > 100 {
			label = label[:100]
		}

		doc := StanceDocument{
			PersonID:         personID,
			ClusterLabel:     strings.TrimSpace(label),
			ClusterEmbedding: embedding,
			Summary:          factText,
			KeyFactIDs:       []int64{factID},
			FactCount:        1,
		}

		stanceID, err := sm.storer.InsertStanceDocument(ctx, doc)
		if err != nil {
			return nil, fmt.Errorf("inserting new stance document: %w", err)
		}

		if err := sm.storer.AddFactToStance(ctx, stanceID, factID); err != nil {
			return nil, fmt.Errorf("adding fact to new stance: %w", err)
		}

		log.Info().
			Int64("stance_id", stanceID).
			Int64("fact_id", factID).
			Int64("person_id", personID).
			Msg("Created new stance document")

		return &StanceProcessResult{
			Verdict:  "NEW_STANCE",
			StanceID: stanceID,
		}, nil
	}

	// Match found: call LLM to determine relationship
	keyFacts, err := sm.storer.GetFactsForStance(ctx, match.ID)
	if err != nil {
		return nil, fmt.Errorf("getting facts for stance %d: %w", match.ID, err)
	}

	llmResp, err := sm.callLLM(ctx, match.Summary, keyFacts, factText)
	if err != nil {
		return nil, fmt.Errorf("LLM stance analysis: %w", err)
	}

	// Add the fact to the stance join table regardless of verdict
	if err := sm.storer.AddFactToStance(ctx, match.ID, factID); err != nil {
		return nil, fmt.Errorf("adding fact to stance %d: %w", match.ID, err)
	}

	result := &StanceProcessResult{
		StanceID:  match.ID,
		Reasoning: llmResp.Reasoning,
	}

	switch llmResp.Verdict {
	case "DUPLICATE":
		result.Verdict = "DUPLICATE"
		log.Info().
			Int64("stance_id", match.ID).
			Int64("fact_id", factID).
			Float64("distance", match.Distance).
			Str("reasoning", llmResp.Reasoning).
			Msg("Fact is duplicate of existing stance")

	case "NEW_INFO":
		result.Verdict = "NEW_INFO"
		newCentroid := updateCentroid(match.ClusterEmbedding, embedding, match.FactCount)
		newFactCount := match.FactCount + 1

		// Keep existing key facts, potentially add new one if list is short
		keyFactIDs := match.KeyFactIDs
		if len(keyFactIDs) < 10 {
			keyFactIDs = append(keyFactIDs, factID)
		}

		if err := sm.storer.UpdateStanceDocument(ctx, match.ID, llmResp.UpdatedSummary, newCentroid, keyFactIDs, newFactCount); err != nil {
			return nil, fmt.Errorf("updating stance document %d: %w", match.ID, err)
		}

		log.Info().
			Int64("stance_id", match.ID).
			Int64("fact_id", factID).
			Float64("distance", match.Distance).
			Int("new_fact_count", newFactCount).
			Msg("Added new info to existing stance")

	case "CONTRADICTION":
		result.Verdict = "CONTRADICTION"
		result.ContradictedFactID = llmResp.ContradictingFactID

		// Record the contradiction
		if llmResp.ContradictingFactID > 0 {
			if err := sm.storer.RecordContradiction(ctx, personID, factID, llmResp.ContradictingFactID, llmResp.Reasoning); err != nil {
				return nil, fmt.Errorf("recording contradiction: %w", err)
			}
		} else {
			log.Warn().
				Int64("stance_id", match.ID).
				Int64("fact_id", factID).
				Msg("LLM detected contradiction but could not identify specific contradicted fact")
		}

		// Update stance with new summary and centroid
		newCentroid := updateCentroid(match.ClusterEmbedding, embedding, match.FactCount)
		newFactCount := match.FactCount + 1

		keyFactIDs := match.KeyFactIDs
		if len(keyFactIDs) < 10 {
			keyFactIDs = append(keyFactIDs, factID)
		}

		if err := sm.storer.UpdateStanceDocument(ctx, match.ID, llmResp.UpdatedSummary, newCentroid, keyFactIDs, newFactCount); err != nil {
			return nil, fmt.Errorf("updating stance document %d after contradiction: %w", match.ID, err)
		}

		log.Warn().
			Int64("stance_id", match.ID).
			Int64("fact_id", factID).
			Int64("contradicted_fact_id", llmResp.ContradictingFactID).
			Float64("distance", match.Distance).
			Str("reasoning", llmResp.Reasoning).
			Msg("Contradiction detected in stance")

	default:
		return nil, fmt.Errorf("unexpected LLM verdict: %q", llmResp.Verdict)
	}

	return result, nil
}

// callLLM sends the stance analysis prompt to the LLM and parses the response.
func (sm *StanceManager) callLLM(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string) (*stanceLLMResponse, error) {
	// Format key facts for the prompt
	var factsBuilder strings.Builder
	for _, f := range keyFacts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`You are analyzing whether a new fact about a political figure updates, duplicates, or contradicts their known stance on a topic.

EXISTING STANCE SUMMARY:
%s

KEY FACTS IN THIS STANCE:
%s

NEW FACT:
%s

Determine the relationship between the new fact and the existing stance:

1. DUPLICATE - The new fact says essentially the same thing as the existing stance (paraphrase, same claim from different source)
2. NEW_INFO - The new fact adds new information to this topic without contradicting the stance (additional detail, update, new development)
3. CONTRADICTION - The new fact directly contradicts a specific claim in the existing stance

CONTRADICTION RULES (apply strictly):
- A contradiction requires the SAME SUBJECT's own statements/actions to conflict
- Promise-breaker framework: identify which is the "promise" and which is the "breaker"
- Authoritative findings (conviction, court ruling) CAN contradict a person's denial
- Accusations, charges, investigations do NOT contradict denials (procedural steps, not findings)
- Earlier "didn't know" → later "knew" is NOT a contradiction (learning over time)
- Earlier "claimed X" → later "denied X" IS a contradiction
- Downplaying ≠ contradicting (admitting imperfections doesn't contradict denying criminal intent)
- Same position in different words = DUPLICATE, not contradiction

Return ONLY valid JSON:
{
  "verdict": "DUPLICATE|NEW_INFO|CONTRADICTION",
  "reasoning": "Brief explanation of your judgment",
  "updated_summary": "If NEW_INFO or CONTRADICTION: updated summary incorporating the new fact. If DUPLICATE: empty string.",
  "contradicting_key_fact_id": 0
}

For CONTRADICTION: set contradicting_key_fact_id to the ID of the specific key fact that is contradicted (from the key facts listed above). If you cannot identify a specific fact, use 0.
For DUPLICATE and NEW_INFO: set contradicting_key_fact_id to 0.`,
		stanceSummary, factsBuilder.String(), newFactText)

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact analysis assistant. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling stance LLM request: %w", err)
	}

	log.Debug().
		Int("key_facts", len(keyFacts)).
		Str("new_fact", truncate(newFactText, 100)).
		Msg("Sending stance analysis to LLM")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM stance request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode stance chat response")
		return nil, fmt.Errorf("decoding stance chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("Stance LLM returned no content")
		return nil, fmt.Errorf("stance LLM returned no content")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	resp, err := parseStanceLLMResponse(content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse stance LLM response")
		return nil, fmt.Errorf("parsing stance LLM response: %w", err)
	}

	return resp, nil
}

// parseStanceLLMResponse parses the LLM's JSON response, handling optional markdown code blocks.
func parseStanceLLMResponse(content string) (*stanceLLMResponse, error) {
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

	var resp stanceLLMResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON in stance response: %w", err)
	}

	// Validate verdict
	switch resp.Verdict {
	case "DUPLICATE", "NEW_INFO", "CONTRADICTION":
		// valid
	default:
		return nil, fmt.Errorf("invalid verdict %q: must be DUPLICATE, NEW_INFO, or CONTRADICTION", resp.Verdict)
	}

	return &resp, nil
}

// GenerateSummary takes a list of facts and generates a summary paragraph plus up to 10 key fact IDs.
// This is intended for backfilling stance documents from existing fact clusters.
func (sm *StanceManager) GenerateSummary(ctx context.Context, facts []StanceFactInfo) (string, []int64, error) {
	if len(facts) == 0 {
		return "", nil, fmt.Errorf("no facts provided for summary generation")
	}

	// Format facts for the prompt
	var factsBuilder strings.Builder
	for _, f := range facts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`Summarize the following facts about a political figure into a concise stance paragraph (2-4 sentences). Also select up to 10 key facts that best represent this stance (by ID).

FACTS:
%s

Return ONLY valid JSON:
{
  "summary": "Concise summary paragraph...",
  "key_fact_ids": [1, 2, 3]
}`, factsBuilder.String())

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact summarization assistant. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling summary LLM request: %w", err)
	}

	log.Debug().Int("fact_count", len(facts)).Msg("Sending facts to LLM for summary generation")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return "", nil, fmt.Errorf("LLM summary request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode summary chat response")
		return "", nil, fmt.Errorf("decoding summary chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("summary LLM returned no content")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)

	// Strip markdown code blocks if present
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

	var resp summaryLLMResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse summary LLM response")
		return "", nil, fmt.Errorf("parsing summary LLM response: %w", err)
	}

	if resp.Summary == "" {
		return "", nil, fmt.Errorf("LLM returned empty summary")
	}

	log.Info().
		Int("fact_count", len(facts)).
		Int("key_facts_selected", len(resp.KeyFactIDs)).
		Msg("Generated stance summary")

	return resp.Summary, resp.KeyFactIDs, nil
}

// updateCentroid computes a weighted average of the old centroid and a new embedding,
// then normalizes the result to unit length.
func updateCentroid(oldCentroid []float32, newEmbedding []float32, oldCount int) []float32 {
	if len(oldCentroid) != len(newEmbedding) {
		log.Error().
			Int("old_len", len(oldCentroid)).
			Int("new_len", len(newEmbedding)).
			Msg("Centroid and embedding dimensions mismatch, returning new embedding")
		return newEmbedding
	}

	result := make([]float32, len(oldCentroid))
	divisor := float32(oldCount + 1)

	for i := range result {
		result[i] = (oldCentroid[i]*float32(oldCount) + newEmbedding[i]) / divisor
	}

	// Normalize to unit length
	var sumSq float64
	for _, v := range result {
		sumSq += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sumSq))
	if norm > 0 {
		for i := range result {
			result[i] /= norm
		}
	}

	return result
}
