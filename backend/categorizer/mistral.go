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

// --- Mistral Specific Implementation ---

const (
	mistralAPIURLDefault = "https://api.mistral.ai/v1/chat/completions"
	mistralModelDefault  = "mistral-small-latest" // Cost-effective choice, supports JSON mode
	// Define character thresholds for chunking (adjust as needed)
	// ~25k chars is a rough proxy for < 8k tokens to leave room for prompt/response
	chunkingThresholdChars = 25000
	chunkSizeChars         = 20000
	overlapChars           = 500
)

// MistralExtractor implements FactExtractor using the Mistral AI API.
type MistralExtractor struct {
	// apiKey is the Mistral API key.
	apiKey string
	// apiURL is the endpoint for the Mistral API.
	apiURL string
	// model is the specific Mistral model to use.
	model string
	// Use the shared prompt template defined in fact.go
	systemPromptTemplate string
	// baseClient handles the underlying HTTP communication.
	baseClient *BaseLLMClient
}

// NewMistralExtractorFromEnv creates a MistralExtractor using the API key from env and default settings.
func NewMistralExtractorFromEnv() (*MistralExtractor, error) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("MISTRAL_API_KEY environment variable not set")
	}
	return &MistralExtractor{
		apiKey: apiKey,
		apiURL: mistralAPIURLDefault,
		model:  mistralModelDefault,
		// Use the shared prompt constant from fact.go
		systemPromptTemplate: backend.FactExtractionSystemPrompt,
		baseClient:           NewBaseLLMClient(),
	}, nil
}

// Internal structs specific to Mistral API request/response
type MistralMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MistralResponseFormat struct {
	Type string `json:"type"` // e.g., "json_object"
}

type MistralRequest struct {
	Model          string                 `json:"model"`
	Messages       []MistralMessage       `json:"messages"`
	Temperature    float64                `json:"temperature"`
	ResponseFormat *MistralResponseFormat `json:"response_format,omitempty"`
}

type MistralChoice struct {
	Index        int            `json:"index"`
	Message      MistralMessage `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type MistralResponse struct {
	Choices []MistralChoice `json:"choices"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// GetChunkingParameters implements the FactExtractor interface.
// Returns parameters suitable for the default Mistral model.
func (m *MistralExtractor) GetChunkingParameters() backend.ChunkingParameters {
	// These values can be adjusted based on the specific model used (m.model)
	// and further testing. Keeping previous values for now.
	return backend.ChunkingParameters{
		ThresholdChars: 25000, // Start chunking above this size
		ChunkSizeChars: 20000, // Aim for chunks of this size
		OverlapChars:   500,   // Character overlap
	}
}

// callMistralAPI performs a single call to the Mistral API with the given content.
func (m *MistralExtractor) callMistralAPI(ctx context.Context, personName, sourceURL, contentText string) ([]backend.Fact, error) {
	systemPrompt := fmt.Sprintf(m.systemPromptTemplate, personName, sourceURL, contentText)

	reqPayload := MistralRequest{
		Model: m.model,
		Messages: []MistralMessage{
			{Role: "system", Content: systemPrompt},
		},
		Temperature:    0.1,
		ResponseFormat: &MistralResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Mistral request payload: %w", err)
	}

	log.Debug().Str("url", m.apiURL).Int("prompt_chars", len(systemPrompt)).Msg("Attempting Mistral API call for fact extraction")
	startTime := time.Now()

	bodyBytes, err := m.baseClient.DoRequest(ctx, m.apiURL, m.apiKey, payloadBytes)

	duration := time.Since(startTime)
	if err != nil {
		// Log the error including duration
		log.Error().Err(err).Dur("duration", duration).Str("url", m.apiURL).Msg("Mistral API call failed")
		// Try to parse specific Mistral error from body if available
		var mistralResp MistralResponse
		if parseErr := json.Unmarshal(bodyBytes, &mistralResp); parseErr == nil && mistralResp.Error != nil {
			return nil, fmt.Errorf("Mistral API returned error: [%s] %s - %s (request status: %w)", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message, err)
		}
		return nil, err // Return original error from DoRequest
	}
	log.Debug().Dur("duration", duration).Int("response_bytes", len(bodyBytes)).Str("url", m.apiURL).Msg("Mistral API call successful")

	var mistralResp MistralResponse
	if err := json.Unmarshal(bodyBytes, &mistralResp); err != nil {
		return nil, fmt.Errorf("failed to decode Mistral API response envelope: %w. Body: %s", err, string(bodyBytes))
	}

	if mistralResp.Error != nil {
		return nil, fmt.Errorf("Mistral API returned error: [%s] %s - %s", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message)
	}

	if len(mistralResp.Choices) == 0 {
		return nil, fmt.Errorf("Mistral API returned no choices")
	}

	jsonContent := mistralResp.Choices[0].Message.Content
	chunkFacts, err := parseFactJSON([]byte(jsonContent), sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from Mistral response content: %w", err)
	}

	return chunkFacts, nil
}

// ExtractFacts implements the FactExtractor interface for Mistral.
// Handles chunking for large content.
func (m *MistralExtractor) ExtractFacts(ctx context.Context, personName string, content extractor.ExtractedContent) ([]backend.Fact, error) {
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
			chunkFacts, err := m.callMistralAPI(ctx, personName, content.URL, chunk)
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
		allExtractedFacts, err = m.callMistralAPI(ctx, personName, content.URL, textContent)
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
