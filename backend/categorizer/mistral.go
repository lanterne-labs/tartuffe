package categorizer

import (
	// "bytes" // Removed unused import
	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
	"encoding/json"
	"fmt"
	"os"
)

// --- Mistral Specific Implementation ---

const (
	mistralAPIURLDefault   = "https://api.mistral.ai/v1/chat/completions"
	mistralModelDefault    = "mistral-small-latest" // Cost-effective choice, supports JSON mode
)

// MistralExtractor implements FactExtractor using the Mistral AI API.
type MistralExtractor struct {
	// apiKey is the Mistral API key.
	apiKey     string
	// apiURL is the endpoint for the Mistral API.
	apiURL     string
	// model is the specific Mistral model to use.
	model      string
	// Use the shared prompt template defined in fact.go
	systemPromptTemplate string
	// baseClient handles the underlying HTTP communication.
	baseClient *baseLLMClient 
}

// NewMistralExtractorFromEnv creates a MistralExtractor using the API key from env and default settings.
func NewMistralExtractorFromEnv() (*MistralExtractor, error) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("MISTRAL_API_KEY environment variable not set")
	}
	return &MistralExtractor{
		apiKey:     apiKey,
		apiURL:     mistralAPIURLDefault,
		model:      mistralModelDefault,
		// Use the shared prompt constant from fact.go
		systemPromptTemplate: backend.FactExtractionSystemPrompt,
		baseClient: newBaseLLMClient(),
	}, nil
}

// Internal structs specific to Mistral API request/response
type mistralMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type mistralResponseFormat struct {
	Type string `json:"type"` // e.g., "json_object"
}

type mistralRequest struct {
	Model           string                `json:"model"`
	Messages        []mistralMessage      `json:"messages"`
	Temperature     float64               `json:"temperature"`
	ResponseFormat  *mistralResponseFormat `json:"response_format,omitempty"`
}

type mistralChoice struct {
	Index        int            `json:"index"`
	Message      mistralMessage `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type mistralResponse struct {
	Choices []mistralChoice `json:"choices"`
	Error   *struct { 
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// ExtractFacts implements the FactExtractor interface for Mistral.
func (m *MistralExtractor) ExtractFacts(personName string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	// Use the systemPromptTemplate field from the struct
	systemPrompt := fmt.Sprintf(m.systemPromptTemplate, personName, content.URL, content.Content)

	// Build the Mistral-specific request payload
	reqPayload := mistralRequest{
		Model: m.model,
		Messages: []mistralMessage{
			{Role: "system", Content: systemPrompt},
		},
		Temperature: 0.1, // Low temperature for factual extraction
		ResponseFormat: &mistralResponseFormat{Type: "json_object"}, // Request JSON mode
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Mistral request payload: %w", err)
	}

	// Use the base client to make the request
	bodyBytes, err := m.baseClient.doRequest(m.apiURL, m.apiKey, payloadBytes)
	if err != nil {
		// Check if the error message contains the body (due to non-200 status)
		// If so, try to parse a structured error from it.
		var mistralResp mistralResponse
		if parseErr := json.Unmarshal(bodyBytes, &mistralResp); parseErr == nil && mistralResp.Error != nil {
			return nil, fmt.Errorf("Mistral API returned error: [%s] %s - %s (request status: %w)", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message, err)
		}
		// Otherwise, return the original error (e.g., network error or status error with non-JSON body)
		return nil, err 
	}

	// Parse the Mistral-specific *outer* response structure
	var mistralResp mistralResponse
	if err := json.Unmarshal(bodyBytes, &mistralResp); err != nil {
		return nil, fmt.Errorf("failed to decode Mistral API response envelope: %w. Body: %s", err, string(bodyBytes))
	}

	// Check for application-level errors returned by Mistral API itself
	if mistralResp.Error != nil {
		return nil, fmt.Errorf("Mistral API returned error: [%s] %s - %s", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message)
	}

	if len(mistralResp.Choices) == 0 {
		return nil, fmt.Errorf("Mistral API returned no choices")
	}

	// Extract the raw JSON content string from the first choice
	jsonContent := mistralResp.Choices[0].Message.Content

	// Use the shared parser to decode the JSON content into Fact structs
	finalFacts, err := parseFactJSON([]byte(jsonContent), content.URL, content.DatePublished)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from Mistral response content: %w", err)
	}

	return finalFacts, nil
} 