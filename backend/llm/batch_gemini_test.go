package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestGeminiBatchProvider creates a GeminiBatchProvider pointing at the given test server.
func newTestGeminiBatchProvider(serverURL string) *GeminiBatchProvider {
	return &GeminiBatchProvider{
		apiKey:     "test-api-key",
		model:      "gemini-2.5-flash",
		baseURL:    serverURL,
		httpClient: http.DefaultClient,
	}
}

func TestGeminiBatchSubmit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request path contains batchGenerateContent
		assert.Contains(t, r.URL.Path, "batchGenerateContent")
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-api-key", r.Header.Get("x-goog-api-key"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse and verify the request body
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req geminiBatchCreateRequest
		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		// Verify inline requests
		inlineReqs := req.Batch.InputConfig.Requests.Requests
		assert.Len(t, inlineReqs, 2)

		// Verify metadata keys match CustomIDs
		assert.Equal(t, "req-1", inlineReqs[0].Metadata.Key)
		assert.Equal(t, "req-2", inlineReqs[1].Metadata.Key)

		// Verify contents were converted
		assert.Len(t, inlineReqs[0].Request.Contents, 1)
		assert.Equal(t, "user", inlineReqs[0].Request.Contents[0].Role)
		assert.Equal(t, "Hello", inlineReqs[0].Request.Contents[0].Parts[0].Text)

		// Verify generation config for JSON format request
		assert.Equal(t, "application/json", inlineReqs[1].Request.GenerationConfig.ResponseMimeType)

		// Return an operation response (matches actual Gemini API format)
		resp := geminiOperation{
			Name: "batches/batch-abc123",
			Metadata: &geminiOperationMetadata{
				State: "BATCH_STATE_PENDING",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := newTestGeminiBatchProvider(server.URL)

	result, err := provider.Submit(context.Background(), []BatchRequest{
		{
			CustomID:    "req-1",
			Messages:    []ChatMessage{{Role: "user", Content: "Hello"}},
			Temperature: 0.7,
		},
		{
			CustomID:       "req-2",
			Messages:       []ChatMessage{{Role: "user", Content: "World"}},
			Temperature:    0.5,
			ResponseFormat: &ResponseFormat{Type: "json_object"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "batches/batch-abc123", result.ProviderBatchID)
	assert.Equal(t, 2, result.RequestCount)
}

func TestGeminiBatchSubmitEmptyRequests(t *testing.T) {
	provider := newTestGeminiBatchProvider("http://unused")
	_, err := provider.Submit(context.Background(), []BatchRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no requests to submit")
}

func TestGeminiBatchStatus(t *testing.T) {
	tests := []struct {
		name          string
		geminiState   string
		expectedState BatchState
		errorMsg      string
	}{
		{
			name:          "pending",
			geminiState:   "BATCH_STATE_PENDING",
			expectedState: BatchStatePending,
		},
		{
			name:          "running",
			geminiState:   "BATCH_STATE_RUNNING",
			expectedState: BatchStateInProgress,
		},
		{
			name:          "succeeded",
			geminiState:   "BATCH_STATE_SUCCEEDED",
			expectedState: BatchStateCompleted,
		},
		{
			name:          "failed with error",
			geminiState:   "BATCH_STATE_FAILED",
			expectedState: BatchStateFailed,
			errorMsg:      "something went wrong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)

				resp := geminiOperation{
					Name: "batches/batch-xyz",
					Metadata: &geminiOperationMetadata{
						State: tt.geminiState,
					},
				}
				if tt.errorMsg != "" {
					resp.Error = &geminiErrorDetail{
						Code:    500,
						Message: tt.errorMsg,
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			provider := newTestGeminiBatchProvider(server.URL)
			status, err := provider.Status(context.Background(), "batches/batch-xyz")

			require.NoError(t, err)
			assert.Equal(t, tt.expectedState, status.State)
			assert.Equal(t, "batches/batch-xyz", status.ProviderBatchID)
			if tt.errorMsg != "" {
				assert.Equal(t, tt.errorMsg, status.ErrorMessage)
			}
		})
	}
}

func TestGeminiBatchRetrieve(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiOperation{
			Name: "batches/batch-done",
			Done: true,
			Metadata: &geminiOperationMetadata{
				State: "BATCH_STATE_SUCCEEDED",
			},
			Response: &geminiOperationResponse{
				InlinedResponses: &geminiInlinedResponsesWrapper{
					InlinedResponses: []geminiInlineResponse{
						{
							Metadata: geminiRequestMetadata{Key: "req-1"},
							Response: &geminiGenerateContentResponse{
								Candidates: []geminiCandidate{
									{
										Content: geminiContent{
											Role:  "model",
											Parts: []geminiPart{{Text: "Response for req-1"}},
										},
									},
								},
							},
						},
						{
							Metadata: geminiRequestMetadata{Key: "req-2"},
							Response: &geminiGenerateContentResponse{
								Candidates: []geminiCandidate{
									{
										Content: geminiContent{
											Role:  "model",
											Parts: []geminiPart{{Text: "Part A"}, {Text: " Part B"}},
										},
									},
								},
							},
						},
						{
							Metadata: geminiRequestMetadata{Key: "req-3"},
							Error: &geminiErrorDetail{
								Code:    400,
								Message: "bad request content",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := newTestGeminiBatchProvider(server.URL)
	items, err := provider.Retrieve(context.Background(), "batches/batch-done")

	require.NoError(t, err)
	require.Len(t, items, 3)

	// First item: successful response
	assert.Equal(t, "req-1", items[0].CustomID)
	require.NotNil(t, items[0].Response)
	assert.Equal(t, "Response for req-1", items[0].Response.Choices[0].Message.Content)
	assert.Empty(t, items[0].Error)

	// Second item: multi-part response concatenated
	assert.Equal(t, "req-2", items[1].CustomID)
	require.NotNil(t, items[1].Response)
	assert.Equal(t, "Part A Part B", items[1].Response.Choices[0].Message.Content)

	// Third item: error response
	assert.Equal(t, "req-3", items[2].CustomID)
	assert.Nil(t, items[2].Response)
	assert.Equal(t, "bad request content", items[2].Error)
}

func TestGeminiBatchRetrieveNotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiOperation{
			Name: "batches/batch-running",
			Metadata: &geminiOperationMetadata{
				State: "BATCH_STATE_RUNNING",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := newTestGeminiBatchProvider(server.URL)
	items, err := provider.Retrieve(context.Background(), "batches/batch-running")

	assert.Nil(t, items)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch is not completed")
}

func TestConvertMessagesToGeminiContents(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "What is Go?"},
		{Role: "assistant", Content: "Go is a programming language."},
		{Role: "user", Content: "Tell me more."},
	}

	contents := convertMessagesToGeminiContents(messages)

	require.Len(t, contents, 4)

	// system -> user
	assert.Equal(t, "user", contents[0].Role)
	assert.Equal(t, "You are a helpful assistant.", contents[0].Parts[0].Text)

	// user stays user
	assert.Equal(t, "user", contents[1].Role)
	assert.Equal(t, "What is Go?", contents[1].Parts[0].Text)

	// assistant -> model
	assert.Equal(t, "model", contents[2].Role)
	assert.Equal(t, "Go is a programming language.", contents[2].Parts[0].Text)

	// user stays user
	assert.Equal(t, "user", contents[3].Role)
	assert.Equal(t, "Tell me more.", contents[3].Parts[0].Text)
}

func TestMapGeminiState(t *testing.T) {
	tests := []struct {
		input    string
		expected BatchState
	}{
		{"BATCH_STATE_PENDING", BatchStatePending},
		{"BATCH_STATE_RUNNING", BatchStateInProgress},
		{"BATCH_STATE_SUCCEEDED", BatchStateCompleted},
		{"BATCH_STATE_FAILED", BatchStateFailed},
		{"BATCH_STATE_CANCELLED", BatchStateFailed},
		{"UNKNOWN_STATE", BatchStatePending}, // unknown defaults to pending
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapGeminiState(tt.input))
		})
	}
}
