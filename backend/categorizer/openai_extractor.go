package categorizer

import (
	// "bytes" // Removed unused import
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/util" // Import util for ChunkText
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
	// Use the shared prompt template defined in fact.go
	systemPromptTemplate string
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
		apiKey: apiKey,
		apiURL: buildChatAPIURL(base),
		model:  model,
		// Use the shared prompt constant from fact.go
		systemPromptTemplate: backend.FactExtractionSystemPrompt,
		baseClient:           NewBaseLLMClient(),
	}, nil
}

// ChatMessage models a single chat message for OpenAI-compatible APIs.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat controls the response format, e.g., JSON mode.
type ResponseFormat struct {
	Type string `json:"type"` // e.g., "json_object"
}

// ChatRequest is the OpenAI-compatible chat completion request.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// ChatChoice models a single choice in the completion response.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatResponse is the OpenAI-compatible completion response envelope.
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// GetChunkingParameters implements the FactExtractor interface.
// Returns parameters suitable for the default configuration.
func (m *OpenAIExtractor) GetChunkingParameters() backend.ChunkingParameters {
	// These values can be adjusted based on the specific model used (m.model)
	// and further testing. Keeping previous values for now.
	return backend.ChunkingParameters{
		ThresholdChars: 25000, // Start chunking above this size
		ChunkSizeChars: 20000, // Aim for chunks of this size
		OverlapChars:   500,   // Character overlap
	}
}

// callChatAPI performs a single call to the OpenAI-compatible Chat Completions API with the given content.
func (m *OpenAIExtractor) callChatAPI(ctx context.Context, personName, sourceURL, contentText string) ([]backend.Fact, error) {
	systemPrompt := fmt.Sprintf(m.systemPromptTemplate, personName, sourceURL, contentText)

	reqPayload := ChatRequest{
		Model: m.model,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
		},
		Temperature:    0.1,
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
		// Try to parse specific API error from body if available
		var chatResp ChatResponse
		if parseErr := json.Unmarshal(bodyBytes, &chatResp); parseErr == nil && chatResp.Error != nil {
			return nil, fmt.Errorf("Chat API returned error: [%s] %s - %s (request status: %w)", chatResp.Error.Code, chatResp.Error.Type, chatResp.Error.Message, err)
		}
		return nil, err // Return original error from DoRequest
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
	chunkFacts, err := parseFactJSON([]byte(jsonContent), sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from chat response content: %w", err)
	}

	return chunkFacts, nil
}

// ExtractFacts implements the FactExtractor interface for OpenAI-compatible chat APIs.
// Handles chunking for large content.
func (m *OpenAIExtractor) ExtractFacts(ctx context.Context, personName string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	textContent := content.Content // Assuming this holds the markdown or main text
	if strings.TrimSpace(textContent) == "" {
		log.Warn().Str("url", content.URL).Msg("ExtractFacts called with empty content.")
		return []backend.Fact{}, nil // Return empty slice, not an error
	}

	// Get chunking parameters from the extractor itself
	chunkParams := m.GetChunkingParameters()

	var allExtractedFacts []backend.Fact

	// Check if content is large enough to require chunking
	if len(textContent) > chunkParams.ThresholdChars {
		log.Info().Str("url", content.URL).Int("content_length", len(textContent)).Int("threshold", chunkParams.ThresholdChars).Msg("Content exceeds threshold, applying chunking for fact extraction.")
		chunks := util.ChunkText(textContent, chunkParams.ChunkSizeChars, chunkParams.OverlapChars)
		log.Info().Str("url", content.URL).Int("chunk_count", len(chunks)).Msg("Content split into chunks")

		for i, chunk := range chunks {
			chunkNum := i + 1
			log.Debug().Str("url", content.URL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("chunk_len", len(chunk)).Msg("Attempting fact extraction for chunk")
			chunkFacts, err := m.callChatAPI(ctx, personName, content.URL, chunk)
			if err != nil {
				log.Error().Err(err).Str("url", content.URL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Msg("Failed to extract facts for chunk")
				continue
			}
			allExtractedFacts = append(allExtractedFacts, chunkFacts...)
			log.Debug().Str("url", content.URL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("facts_extracted", len(chunkFacts)).Msg("Successfully processed chunk")
		}

	} else {
		// Content is small enough, process directly
		log.Debug().Str("url", content.URL).Int("content_length", len(textContent)).Int("threshold", chunkParams.ThresholdChars).Msg("Content below threshold, processing directly without chunking.")
		var err error
		allExtractedFacts, err = m.callChatAPI(ctx, personName, content.URL, textContent)
		if err != nil {
			return nil, fmt.Errorf("failed to extract facts from content for url %s: %w", content.URL, err)
		}
	}

	// --- Post-processing: Deduplicate ---

	// Simple deduplication based on NormalizedFactText (if chunking created duplicates)
	finalFactsMap := make(map[string]backend.Fact)
	for _, fact := range allExtractedFacts {
		if _, exists := finalFactsMap[fact.NormalizedFactText]; !exists {
			finalFactsMap[fact.NormalizedFactText] = fact
		}
	}
	finalFacts := make([]backend.Fact, 0, len(finalFactsMap))
	for _, fact := range finalFactsMap {
		finalFacts = append(finalFacts, fact)
	}

	log.Info().Str("url", content.URL).Int("initial_facts", len(allExtractedFacts)).Int("final_facts", len(finalFacts)).Msg("Fact extraction completed")
	return finalFacts, nil
}
