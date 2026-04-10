package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMistralBatchProvider(serverURL string) *MistralBatchProvider {
	return &MistralBatchProvider{
		apiKey:     "test-api-key",
		model:      "mistral-small-2603",
		baseURL:    serverURL,
		httpClient: http.DefaultClient,
	}
}

func TestMistralBatchSubmit(t *testing.T) {
	var (
		uploadCalled bool
		createCalled bool
		uploadedJSONL []byte
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/files":
			uploadCalled = true
			require.NoError(t, r.ParseMultipartForm(1<<20))
			assert.Equal(t, "batch", r.FormValue("purpose"))

			file, hdr, err := r.FormFile("file")
			require.NoError(t, err)
			defer file.Close()
			assert.Contains(t, hdr.Filename, "batch-")
			assert.Contains(t, hdr.Filename, ".jsonl")

			body, err := io.ReadAll(file)
			require.NoError(t, err)
			uploadedJSONL = body

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mistralFileUploadResponse{
				ID:       "file-abc",
				Filename: hdr.Filename,
				Purpose:  "batch",
			})

		case r.Method == "POST" && r.URL.Path == "/v1/batch/jobs":
			createCalled = true
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var req mistralBatchCreateRequest
			require.NoError(t, json.Unmarshal(body, &req))

			assert.Equal(t, []string{"file-abc"}, req.InputFiles)
			assert.Equal(t, "/v1/chat/completions", req.Endpoint)
			assert.Equal(t, "mistral-small-2603", req.Model)
			assert.Contains(t, req.Metadata["display_name"], "dm-batch-")

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mistralBatchJob{
				ID:     "batch-xyz",
				Status: "QUEUED",
			})

		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)

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
	assert.True(t, uploadCalled, "expected file upload to be called")
	assert.True(t, createCalled, "expected batch job create to be called")
	assert.Equal(t, "batch-xyz", result.ProviderBatchID)
	assert.Equal(t, 2, result.RequestCount)

	// Verify the JSONL content sent during upload
	lines := bytes.Split(bytes.TrimSpace(uploadedJSONL), []byte("\n"))
	require.Len(t, lines, 2)

	var line1, line2 mistralInputLine
	require.NoError(t, json.Unmarshal(lines[0], &line1))
	require.NoError(t, json.Unmarshal(lines[1], &line2))

	assert.Equal(t, "req-1", line1.CustomID)
	assert.Equal(t, "mistral-small-2603", line1.Body.Model)
	assert.Equal(t, "Hello", line1.Body.Messages[0].Content)
	assert.Equal(t, 0.7, line1.Body.Temperature)
	assert.Nil(t, line1.Body.ResponseFormat)

	assert.Equal(t, "req-2", line2.CustomID)
	require.NotNil(t, line2.Body.ResponseFormat)
	assert.Equal(t, "json_object", line2.Body.ResponseFormat.Type)
}

func TestMistralBatchSubmitEmptyRequests(t *testing.T) {
	provider := newTestMistralBatchProvider("http://unused")
	_, err := provider.Submit(context.Background(), []BatchRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no requests to submit")
}

func TestMistralBatchStatus(t *testing.T) {
	tests := []struct {
		name          string
		mistralStatus string
		expectedState BatchState
		errorMsg      string
	}{
		{"queued", "QUEUED", BatchStatePending, ""},
		{"running", "RUNNING", BatchStateInProgress, ""},
		{"cancellation requested", "CANCELLATION_REQUESTED", BatchStateInProgress, ""},
		{"success", "SUCCESS", BatchStateCompleted, ""},
		{"failed with error", "FAILED", BatchStateFailed, "something went wrong"},
		{"cancelled", "CANCELLED", BatchStateFailed, ""},
		{"timeout", "TIMEOUT_EXCEEDED", BatchStateExpired, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/v1/batch/jobs/batch-xyz", r.URL.Path)
				assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

				job := mistralBatchJob{
					ID:                "batch-xyz",
					Status:            tt.mistralStatus,
					SucceededRequests: 5,
					FailedRequests:    1,
				}
				if tt.errorMsg != "" {
					job.Errors = []mistralBatchErr{{Message: tt.errorMsg}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(job)
			}))
			defer server.Close()

			provider := newTestMistralBatchProvider(server.URL)
			status, err := provider.Status(context.Background(), "batch-xyz")
			require.NoError(t, err)
			assert.Equal(t, tt.expectedState, status.State)
			assert.Equal(t, "batch-xyz", status.ProviderBatchID)
			assert.Equal(t, 5, status.CompletedCount)
			assert.Equal(t, 1, status.FailedCount)
			if tt.errorMsg != "" {
				assert.Equal(t, tt.errorMsg, status.ErrorMessage)
			}
		})
	}
}

func TestMistralBatchRetrieve(t *testing.T) {
	outputFile := "file-out"

	successBody := ChatResponse{
		Choices: []ChatChoice{
			{
				Index:   0,
				Message: ChatMessage{Role: "assistant", Content: "Response for req-1"},
			},
		},
	}

	outputLine1 := mistralOutputLine{
		CustomID: "req-1",
		Response: &mistralOutputResp{
			StatusCode: 200,
			Body:       successBody,
		},
	}
	outputLine2 := mistralOutputLine{
		CustomID: "req-2",
		Error: &mistralOutputErrBody{
			Message: "bad request content",
		},
	}

	var jsonlBuf bytes.Buffer
	enc := json.NewEncoder(&jsonlBuf)
	require.NoError(t, enc.Encode(outputLine1))
	require.NoError(t, enc.Encode(outputLine2))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/batch/jobs/batch-done":
			of := outputFile
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mistralBatchJob{
				ID:         "batch-done",
				Status:     "SUCCESS",
				OutputFile: &of,
			})
		case r.Method == "GET" && r.URL.Path == "/v1/files/file-out/content":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write(jsonlBuf.Bytes())
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)
	items, err := provider.Retrieve(context.Background(), "batch-done")
	require.NoError(t, err)
	require.Len(t, items, 2)

	assert.Equal(t, "req-1", items[0].CustomID)
	require.NotNil(t, items[0].Response)
	assert.Equal(t, "Response for req-1", items[0].Response.Choices[0].Message.Content)
	assert.Empty(t, items[0].Error)

	assert.Equal(t, "req-2", items[1].CustomID)
	assert.Nil(t, items[1].Response)
	assert.Equal(t, "bad request content", items[1].Error)
}

func TestMistralBatchRetrieveNotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mistralBatchJob{
			ID:     "batch-running",
			Status: "RUNNING",
		})
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)
	items, err := provider.Retrieve(context.Background(), "batch-running")
	assert.Nil(t, items)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch is not completed")
}

func TestMistralBatchRetrieveMissingOutputFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mistralBatchJob{
			ID:     "batch-nofile",
			Status: "SUCCESS",
		})
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)
	_, err := provider.Retrieve(context.Background(), "batch-nofile")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no output_file")
}

func TestMistralBatchRetrieveWithErrorFile(t *testing.T) {
	// Verifies that error_file entries override successful response entries with the same custom_id
	// (defensive: the API should not normally report the same custom_id in both, but we want to
	// keep the error surface rather than silently return a stale success).
	outputFile := "file-out"
	errorFile := "file-err"

	outputLine := mistralOutputLine{
		CustomID: "req-1",
		Response: &mistralOutputResp{
			StatusCode: 200,
			Body: ChatResponse{
				Choices: []ChatChoice{{Index: 0, Message: ChatMessage{Role: "assistant", Content: "ok"}}},
			},
		},
	}
	errLine := mistralOutputLine{
		CustomID: "req-2",
		Error:    &mistralOutputErrBody{Message: "rate limited"},
	}

	var outBuf, errBuf bytes.Buffer
	require.NoError(t, json.NewEncoder(&outBuf).Encode(outputLine))
	require.NoError(t, json.NewEncoder(&errBuf).Encode(errLine))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/batch/jobs/batch-merged":
			of, ef := outputFile, errorFile
			_ = json.NewEncoder(w).Encode(mistralBatchJob{
				ID:         "batch-merged",
				Status:     "SUCCESS",
				OutputFile: &of,
				ErrorFile:  &ef,
			})
		case r.URL.Path == "/v1/files/file-out/content":
			_, _ = w.Write(outBuf.Bytes())
		case r.URL.Path == "/v1/files/file-err/content":
			_, _ = w.Write(errBuf.Bytes())
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)
	items, err := provider.Retrieve(context.Background(), "batch-merged")
	require.NoError(t, err)
	require.Len(t, items, 2)

	byID := map[string]BatchResponseItem{}
	for _, it := range items {
		byID[it.CustomID] = it
	}
	assert.Equal(t, "ok", byID["req-1"].Response.Choices[0].Message.Content)
	assert.Equal(t, "rate limited", byID["req-2"].Error)
	assert.Nil(t, byID["req-2"].Response)
}

func TestMistralBatchErrorStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer server.Close()

	provider := newTestMistralBatchProvider(server.URL)
	_, err := provider.Status(context.Background(), "batch-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "invalid api key")
}

func TestParseMistralOutputJSONL(t *testing.T) {
	jsonl := `{"custom_id":"a","response":{"status_code":200,"body":{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}}}
{"custom_id":"b","error":{"message":"boom"}}

{"custom_id":"c","response":{"status_code":500,"body":{}}}
`
	items, err := parseMistralOutputJSONL([]byte(jsonl))
	require.NoError(t, err)
	require.Len(t, items, 3)

	assert.Equal(t, "a", items[0].CustomID)
	require.NotNil(t, items[0].Response)
	assert.Equal(t, "hello", items[0].Response.Choices[0].Message.Content)

	assert.Equal(t, "b", items[1].CustomID)
	assert.Nil(t, items[1].Response)
	assert.Equal(t, "boom", items[1].Error)

	assert.Equal(t, "c", items[2].CustomID)
	assert.Nil(t, items[2].Response)
	assert.Contains(t, items[2].Error, "500")
}

func TestParseMistralOutputJSONLInvalid(t *testing.T) {
	items, err := parseMistralOutputJSONL([]byte("not json\n"))
	assert.Nil(t, items)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid jsonl")
}

func TestMapMistralState(t *testing.T) {
	tests := []struct {
		input    string
		expected BatchState
	}{
		{"QUEUED", BatchStatePending},
		{"RUNNING", BatchStateInProgress},
		{"CANCELLATION_REQUESTED", BatchStateInProgress},
		{"SUCCESS", BatchStateCompleted},
		{"FAILED", BatchStateFailed},
		{"CANCELLED", BatchStateFailed},
		{"TIMEOUT_EXCEEDED", BatchStateExpired},
		{"UNKNOWN_THING", BatchStatePending},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapMistralState(tt.input))
		})
	}
}

// Sanity: ensure the provider implements the interface.
func TestMistralBatchProviderImplementsInterface(t *testing.T) {
	var _ BatchProvider = (*MistralBatchProvider)(nil)
}

// Ensure JSONL serialization does not HTML-escape angle brackets in prompts.
func TestMistralJSONLNoHTMLEscape(t *testing.T) {
	provider := newTestMistralBatchProvider("http://unused")
	data, err := provider.buildInputJSONL([]BatchRequest{
		{
			CustomID: "x",
			Messages: []ChatMessage{{Role: "user", Content: "1 < 2 && 3 > 0"}},
		},
	})
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), "1 < 2 && 3 > 0"),
		"json encoder should not HTML-escape content: %s", string(data))
}

