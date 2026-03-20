package categorizer

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// --- OpenAI-compatible Chat Completion Implementation ---

const (
	// chunking parameters kept generic
	// ~25k chars is a rough proxy for < 8k tokens to leave room for prompt/response
	chunkingThresholdChars = 25000
	chunkSizeChars         = 20000
	overlapChars           = 500
)

// openAIChatModelDefault is a sensible default, can be overridden via OPEN_AI_CHAT_MODEL
const openAIChatModelDefault = "gpt-4o-mini"

// buildChatAPIURL composes the full chat completions endpoint from the OPEN_AI_API base.
func buildChatAPIURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// OpenAIExtractor implements FactExtractor using an OpenAI-compatible Chat Completions API.
type OpenAIExtractor struct {
	// apiKey is the API key to authenticate requests.
	apiKey string
	// apiURL is the full endpoint for chat completions.
	apiURL string
	// model is the model to use for chat completions.
	model string
	// baseClient handles the underlying HTTP communication.
	baseClient *BaseLLMClient
}

// NewOpenAIExtractorFromEnv creates an OpenAIExtractor using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API (base, e.g., https://api.openai.com/v1)
// Optional: OPEN_AI_CHAT_MODEL (defaults to openAIChatModelDefault)
func NewOpenAIExtractorFromEnv() (*OpenAIExtractor, error) {
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
		model = openAIChatModelDefault
	}
	return &OpenAIExtractor{
		apiKey:     apiKey,
		apiURL:     buildChatAPIURL(base),
		model:      model,
		baseClient: NewBaseLLMClient(),
	}, nil
}

// NOTE: Chat types (ChatMessage, ChatRequest, ChatResponse, etc.) are now
// defined as type aliases in base_llm_client.go pointing to backend/llm package.
// This allows shared usage across packages without import cycles.

// GetChunkingParameters implements the FactExtractor interface.
// Returns parameters suitable for the default configuration.
func (m *OpenAIExtractor) GetChunkingParameters() backend.ChunkingParameters {
	return backend.ChunkingParameters{
		ThresholdChars: 25000, // Start chunking above this size
		ChunkSizeChars: 20000, // Aim for chunks of this size
		OverlapChars:   500,   // Character overlap
	}
}

// ExtractFactsWithConfig extracts facts using the unified core prompt with optional plugin instructions.
// This is the primary extraction method - all other methods delegate to this one.
func (m *OpenAIExtractor) ExtractFactsWithConfig(ctx context.Context, cfg core.FactExtractorConfig) ([]backend.Fact, error) {
	if strings.TrimSpace(cfg.Content) == "" {
		log.Warn().Str("url", cfg.SourceURL).Msg("ExtractFactsWithConfig called with empty content")
		return []backend.Fact{}, nil
	}

	chunkParams := m.GetChunkingParameters()
	var allExtractedFacts []backend.Fact

	if len(cfg.Content) > chunkParams.ThresholdChars {
		log.Info().Str("url", cfg.SourceURL).Int("content_length", len(cfg.Content)).Int("threshold", chunkParams.ThresholdChars).Msg("Content exceeds threshold, applying chunking for fact extraction")
		chunks := util.ChunkText(cfg.Content, chunkParams.ChunkSizeChars, chunkParams.OverlapChars)
		log.Info().Str("url", cfg.SourceURL).Int("chunk_count", len(chunks)).Msg("Content split into chunks")

		for i, chunk := range chunks {
			chunkNum := i + 1
			log.Debug().Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("chunk_len", len(chunk)).Msg("Attempting fact extraction for chunk")

			chunkCfg := core.FactExtractorConfig{
				TargetName:         cfg.TargetName,
				SourceURL:          cfg.SourceURL,
				Content:            chunk,
				PluginInstructions: cfg.PluginInstructions,
			}
			prompt := core.BuildFactExtractionPrompt(chunkCfg)
			chunkFacts, err := m.callChatAPIWithPrompt(ctx, prompt, cfg.SourceURL)
			if err != nil {
				log.Error().Err(err).Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Msg("Failed to extract facts for chunk")
				continue
			}
			allExtractedFacts = append(allExtractedFacts, chunkFacts...)
			log.Debug().Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("facts_extracted", len(chunkFacts)).Msg("Successfully processed chunk")
		}
	} else {
		log.Debug().Str("url", cfg.SourceURL).Int("content_length", len(cfg.Content)).Int("threshold", chunkParams.ThresholdChars).Msg("Content below threshold, processing directly without chunking")

		prompt := core.BuildFactExtractionPrompt(cfg)
		var err error
		allExtractedFacts, err = m.callChatAPIWithPrompt(ctx, prompt, cfg.SourceURL)
		if err != nil {
			return nil, fmt.Errorf("failed to extract facts from content for url %s: %w", cfg.SourceURL, err)
		}
	}

	// Consolidate: group by (CanonicalVerb, EmbeddingText), keep the most specific (longest NormalizedFactText)
	type groupKey struct {
		Verb      string
		Embedding string
	}
	groupMap := make(map[groupKey]backend.Fact)
	for _, fact := range allExtractedFacts {
		key := groupKey{Verb: fact.CanonicalVerb, Embedding: fact.EmbeddingText}
		existing, exists := groupMap[key]
		if !exists || len(fact.NormalizedFactText) > len(existing.NormalizedFactText) {
			groupMap[key] = fact
		}
	}
	finalFacts := make([]backend.Fact, 0, len(groupMap))
	for _, fact := range groupMap {
		finalFacts = append(finalFacts, fact)
	}

	log.Info().Str("url", cfg.SourceURL).Int("initial_facts", len(allExtractedFacts)).Int("final_facts", len(finalFacts)).Msg("Fact extraction completed")
	return finalFacts, nil
}

// ExtractFacts extracts facts from content about a person/target using the core prompt.
// This is a convenience method that wraps ExtractFactsWithConfig.
func (m *OpenAIExtractor) ExtractFacts(ctx context.Context, targetName string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	cfg := core.FactExtractorConfig{
		TargetName: targetName,
		SourceURL:  content.URL,
		Content:    content.Content,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// ExtractFactsWithType extracts facts using plugin instructions appropriate for the target type.
// targetType should be "person" or "party". Falls back to default for unknown types.
func (m *OpenAIExtractor) ExtractFactsWithType(ctx context.Context, targetName string, targetType string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	var pluginInstructions string
	switch targetType {
	case "party":
		pluginInstructions = core.PartyInstructions
	case "person":
		// Person extraction uses core prompt without additional instructions
		pluginInstructions = ""
	default:
		pluginInstructions = ""
	}

	cfg := core.FactExtractorConfig{
		TargetName:         targetName,
		SourceURL:          content.URL,
		Content:            content.Content,
		PluginInstructions: pluginInstructions,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// ExtractFactsForPersonWithParties extracts facts about a person while providing party context.
// The LLM is informed about the person's party affiliations to help contextualize statements.
// affiliatedParties should be a list of party names the person is affiliated with.
func (m *OpenAIExtractor) ExtractFactsForPersonWithParties(ctx context.Context, personName string, affiliatedParties []string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	partiesStr := strings.Join(affiliatedParties, ", ")
	if partiesStr == "" {
		partiesStr = "(none)"
	}

	pluginInstructions := fmt.Sprintf(core.PersonWithPartiesInstructions, partiesStr)

	cfg := core.FactExtractorConfig{
		TargetName:         personName,
		SourceURL:          content.URL,
		Content:            content.Content,
		PluginInstructions: pluginInstructions,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// callChatAPIWithPrompt performs a single call to the Chat API with a pre-formatted prompt.
func (m *OpenAIExtractor) callChatAPIWithPrompt(ctx context.Context, systemPrompt string, sourceURL string) ([]backend.Fact, error) {
	reqPayload := ChatRequest{
		Model: m.model,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
		},
		Temperature:    0.1,
		MaxTokens:      16384,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request payload: %w", err)
	}

	log.Debug().Str("url", m.apiURL).Int("prompt_chars", len(systemPrompt)).Msg("Attempting LLM chat API call for fact extraction")
	startTime := time.Now()

	bodyBytes, err := m.baseClient.DoRequest(ctx, m.apiURL, m.apiKey, payloadBytes)

	duration := time.Since(startTime)
	if err != nil {
		log.Error().Err(err).Dur("duration", duration).Str("url", m.apiURL).Msg("Chat API call failed")
		var chatResp ChatResponse
		if parseErr := json.Unmarshal(bodyBytes, &chatResp); parseErr == nil && chatResp.Error != nil {
			return nil, fmt.Errorf("Chat API returned error: [%s] %s - %s (request status: %w)", chatResp.Error.Code, chatResp.Error.Type, chatResp.Error.Message, err)
		}
		return nil, err
	}
	log.Debug().Dur("duration", duration).Int("response_bytes", len(bodyBytes)).Str("url", m.apiURL).Msg("Chat API call successful")

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode chat API response envelope: %w. Body: %s", err, string(bodyBytes))
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("Chat API returned error: [%s] %s - %s", chatResp.Error.Code, chatResp.Error.Type, chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("Chat API returned no choices")
	}

	jsonContent := chatResp.Choices[0].Message.Content
	chunkFacts, err := ParseFactJSON([]byte(jsonContent), sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from chat response content: %w", err)
	}

	return chunkFacts, nil
}
