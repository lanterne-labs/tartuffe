package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	geminiBaseURL      = "https://generativelanguage.googleapis.com"
	geminiAPIVersion   = "v1beta"
	geminiDefaultModel = "gemini-2.5-flash"
)

// GeminiBatchProvider implements BatchProvider for Google's Gemini API.
type GeminiBatchProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewGeminiBatchProvider creates a new Gemini batch provider.
func NewGeminiBatchProvider(apiKey, model string) *GeminiBatchProvider {
	if model == "" {
		model = geminiDefaultModel
	}
	return &GeminiBatchProvider{
		apiKey:     apiKey,
		model:      model,
		baseURL:    geminiBaseURL,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (g *GeminiBatchProvider) ProviderName() string { return "gemini" }

// Submit sends a batch of requests to the Gemini Batch API.
func (g *GeminiBatchProvider) Submit(ctx context.Context, requests []BatchRequest) (*BatchSubmitResult, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("no requests to submit")
	}

	// Build inline requests
	inlineRequests := make([]geminiInlineRequest, 0, len(requests))
	for _, req := range requests {
		contents := convertMessagesToGeminiContents(req.Messages)
		genConfig := geminiGenerationConfig{
			Temperature: req.Temperature,
		}
		if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
			genConfig.ResponseMimeType = "application/json"
		}
		inlineRequests = append(inlineRequests, geminiInlineRequest{
			Request: geminiGenerateContentRequest{
				Contents:         contents,
				GenerationConfig: &genConfig,
			},
			Metadata: geminiRequestMetadata{Key: req.CustomID},
		})
	}

	model := g.model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}

	body := geminiBatchCreateRequest{
		Batch: geminiBatchSpec{
			DisplayName: fmt.Sprintf("dm-batch-%d", time.Now().Unix()),
			InputConfig: geminiInputConfig{
				Requests: geminiInlineRequests{
					Requests: inlineRequests,
				},
			},
		},
	}

	url := fmt.Sprintf("%s/%s/%s:batchGenerateContent", g.baseURL, geminiAPIVersion, model)
	respBody, err := g.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to submit batch: %w", err)
	}

	var op geminiOperation
	if err := json.Unmarshal(respBody, &op); err != nil {
		return nil, fmt.Errorf("failed to parse submit response: %w (body: %s)", err, truncateBytes(respBody, 500))
	}

	batchName := op.batchName()
	if batchName == "" {
		return nil, fmt.Errorf("batch creation returned empty name (body: %s)", truncateBytes(respBody, 500))
	}

	log.Info().Str("batch_name", batchName).Int("requests", len(requests)).Msg("Gemini batch submitted")

	return &BatchSubmitResult{
		ProviderBatchID: batchName,
		RequestCount:    len(requests),
	}, nil
}

// Status checks the current state of a Gemini batch.
func (g *GeminiBatchProvider) Status(ctx context.Context, providerBatchID string) (*BatchStatus, error) {
	url := fmt.Sprintf("%s/%s/%s", g.baseURL, geminiAPIVersion, providerBatchID)
	respBody, err := g.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch status: %w", err)
	}

	var op geminiOperation
	if err := json.Unmarshal(respBody, &op); err != nil {
		return nil, fmt.Errorf("failed to parse status response: %w", err)
	}

	state := mapGeminiState(op.state())
	status := &BatchStatus{
		ProviderBatchID: providerBatchID,
		State:           state,
	}
	if op.Error != nil {
		status.ErrorMessage = op.Error.Message
	}

	return status, nil
}

// Retrieve fetches the results of a completed Gemini batch.
func (g *GeminiBatchProvider) Retrieve(ctx context.Context, providerBatchID string) ([]BatchResponseItem, error) {
	url := fmt.Sprintf("%s/%s/%s", g.baseURL, geminiAPIVersion, providerBatchID)
	respBody, err := g.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve batch: %w", err)
	}

	var op geminiOperation
	if err := json.Unmarshal(respBody, &op); err != nil {
		return nil, fmt.Errorf("failed to parse retrieve response: %w", err)
	}

	state := mapGeminiState(op.state())
	if state != BatchStateCompleted {
		return nil, fmt.Errorf("batch is not completed (state: %s)", op.state())
	}

	// Try inline responses from operation response wrapper
	if op.Response != nil && op.Response.InlinedResponses != nil && len(op.Response.InlinedResponses.InlinedResponses) > 0 {
		return g.parseInlineResponses(op.Response.InlinedResponses.InlinedResponses)
	}

	return nil, fmt.Errorf("no results found in batch response")
}

// parseInlineResponses converts Gemini inline responses to BatchResponseItems.
func (g *GeminiBatchProvider) parseInlineResponses(responses []geminiInlineResponse) ([]BatchResponseItem, error) {
	items := make([]BatchResponseItem, 0, len(responses))
	for _, resp := range responses {
		item := BatchResponseItem{
			CustomID: resp.Metadata.Key,
		}

		if resp.Error != nil {
			item.Error = resp.Error.Message
		} else if resp.Response != nil && len(resp.Response.Candidates) > 0 {
			// Convert Gemini response to our ChatResponse format
			content := extractGeminiTextContent(resp.Response.Candidates[0])
			item.Response = &ChatResponse{
				Choices: []ChatChoice{
					{
						Index:   0,
						Message: ChatMessage{Role: "assistant", Content: content},
					},
				},
			}
		} else {
			item.Error = "empty response from Gemini"
		}

		items = append(items, item)
	}
	return items, nil
}

// doRequest performs an HTTP request to the Gemini API.
func (g *GeminiBatchProvider) doRequest(ctx context.Context, method, url string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("x-goog-api-key", g.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(truncateBytes(respBody, 500))
		if OnCreditError != nil && isCreditError(resp.StatusCode, bodyStr) {
			OnCreditError("gemini", bodyStr)
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, bodyStr)
	}

	return respBody, nil
}

// convertMessagesToGeminiContents converts ChatMessages to Gemini Content format.
func convertMessagesToGeminiContents(messages []ChatMessage) []geminiContent {
	contents := make([]geminiContent, 0, len(messages))
	for _, msg := range messages {
		role := msg.Role
		// Gemini uses "user" and "model" roles; map "system" and "assistant" accordingly
		switch role {
		case "system":
			role = "user"
		case "assistant":
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Content}},
		})
	}
	return contents
}

// extractGeminiTextContent extracts text from a Gemini candidate response.
func extractGeminiTextContent(candidate geminiCandidate) string {
	var parts []string
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "")
}

// mapGeminiState maps Gemini batch states to our BatchState type.
// The Gemini Batch API uses BATCH_STATE_* prefixes.
func mapGeminiState(state string) BatchState {
	switch state {
	case "BATCH_STATE_PENDING":
		return BatchStatePending
	case "BATCH_STATE_RUNNING":
		return BatchStateInProgress
	case "BATCH_STATE_SUCCEEDED":
		return BatchStateCompleted
	case "BATCH_STATE_FAILED":
		return BatchStateFailed
	case "BATCH_STATE_CANCELLED":
		return BatchStateFailed
	default:
		log.Warn().Str("state", state).Msg("Unknown Gemini batch state")
		return BatchStatePending
	}
}

func truncateBytes(b []byte, maxLen int) []byte {
	if len(b) <= maxLen {
		return b
	}
	return b[:maxLen]
}

// --- Gemini API types ---

type geminiBatchCreateRequest struct {
	Batch geminiBatchSpec `json:"batch"`
}

type geminiBatchSpec struct {
	DisplayName string            `json:"display_name,omitempty"`
	InputConfig geminiInputConfig `json:"input_config"`
}

type geminiInputConfig struct {
	Requests geminiInlineRequests `json:"requests"`
}

type geminiInlineRequests struct {
	Requests []geminiInlineRequest `json:"requests"`
}

type geminiInlineRequest struct {
	Request  geminiGenerateContentRequest `json:"request"`
	Metadata geminiRequestMetadata        `json:"metadata"`
}

type geminiRequestMetadata struct {
	Key string `json:"key"`
}

type geminiGenerateContentRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generation_config,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	Temperature      float64 `json:"temperature,omitempty"`
	ResponseMimeType string  `json:"response_mime_type,omitempty"`
}

// geminiOperation is the top-level response from the Gemini Batch API.
// All endpoints (submit, status, retrieve) return this operation wrapper.
type geminiOperation struct {
	Name     string                   `json:"name"`
	Done     bool                     `json:"done"`
	Metadata *geminiOperationMetadata `json:"metadata,omitempty"`
	Response *geminiOperationResponse `json:"response,omitempty"`
	Error    *geminiErrorDetail       `json:"error,omitempty"`
}

func (op *geminiOperation) batchName() string {
	return op.Name
}

func (op *geminiOperation) state() string {
	if op.Metadata != nil {
		return op.Metadata.State
	}
	return ""
}

type geminiOperationMetadata struct {
	State      string            `json:"state"`
	BatchStats *geminiBatchStats `json:"batchStats,omitempty"`
}

type geminiBatchStats struct {
	RequestCount           string `json:"requestCount"`
	SuccessfulRequestCount string `json:"successfulRequestCount"`
	FailedRequestCount     string `json:"failedRequestCount"`
}

// geminiOperationResponse wraps the batch results.
// The actual responses are doubly nested: response.inlinedResponses.inlinedResponses
type geminiOperationResponse struct {
	InlinedResponses *geminiInlinedResponsesWrapper `json:"inlinedResponses,omitempty"`
}

type geminiInlinedResponsesWrapper struct {
	InlinedResponses []geminiInlineResponse `json:"inlinedResponses,omitempty"`
}

type geminiInlineResponse struct {
	Response *geminiGenerateContentResponse `json:"response,omitempty"`
	Metadata geminiRequestMetadata          `json:"metadata"`
	Error    *geminiErrorDetail             `json:"error,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
