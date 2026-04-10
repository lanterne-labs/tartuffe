package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	mistralBaseURL = "https://api.mistral.ai"
	// Pinned to Mistral Small 4 (released 2026-03-16). We intentionally do NOT
	// use the "mistral-small-latest" alias because it currently points to
	// Devstral Small 2 (a coding-focused variant), not Mistral Small 4.
	mistralDefaultModel = "mistral-small-2603"
	mistralChatEndpoint = "/v1/chat/completions"
)

// MistralBatchProvider implements BatchProvider for Mistral's Batch API.
// It uses the file-based flow: upload a JSONL file, create a batch job that
// references the file, poll for completion, then download the output JSONL.
type MistralBatchProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewMistralBatchProvider creates a new Mistral batch provider.
func NewMistralBatchProvider(apiKey, model string) *MistralBatchProvider {
	if model == "" {
		model = mistralDefaultModel
	}
	return &MistralBatchProvider{
		apiKey:     apiKey,
		model:      model,
		baseURL:    mistralBaseURL,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (m *MistralBatchProvider) ProviderName() string { return "mistral" }

// Submit uploads a JSONL file to Mistral Files API and creates a batch job.
func (m *MistralBatchProvider) Submit(ctx context.Context, requests []BatchRequest) (*BatchSubmitResult, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("no requests to submit")
	}

	jsonl, err := m.buildInputJSONL(requests)
	if err != nil {
		return nil, fmt.Errorf("failed to build input jsonl: %w", err)
	}

	fileID, err := m.uploadBatchFile(ctx, jsonl)
	if err != nil {
		return nil, fmt.Errorf("failed to upload batch input file: %w", err)
	}

	createBody := mistralBatchCreateRequest{
		InputFiles: []string{fileID},
		Endpoint:   mistralChatEndpoint,
		Model:      m.model,
		Metadata: map[string]string{
			"display_name": fmt.Sprintf("dm-batch-%d", time.Now().Unix()),
		},
	}

	respBody, err := m.doJSONRequest(ctx, "POST", m.baseURL+"/v1/batch/jobs", createBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create batch job: %w", err)
	}

	var job mistralBatchJob
	if err := json.Unmarshal(respBody, &job); err != nil {
		return nil, fmt.Errorf("failed to parse batch job response: %w (body: %s)", err, truncateBytes(respBody, 500))
	}
	if job.ID == "" {
		return nil, fmt.Errorf("batch creation returned empty id (body: %s)", truncateBytes(respBody, 500))
	}

	log.Info().
		Str("batch_id", job.ID).
		Str("input_file_id", fileID).
		Int("requests", len(requests)).
		Msg("Mistral batch submitted")

	return &BatchSubmitResult{
		ProviderBatchID: job.ID,
		RequestCount:    len(requests),
	}, nil
}

// Status checks the current state of a Mistral batch.
func (m *MistralBatchProvider) Status(ctx context.Context, providerBatchID string) (*BatchStatus, error) {
	job, err := m.getJob(ctx, providerBatchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch status: %w", err)
	}

	state := mapMistralState(job.Status)
	status := &BatchStatus{
		ProviderBatchID: providerBatchID,
		State:           state,
		CompletedCount:  job.SucceededRequests,
		FailedCount:     job.FailedRequests,
	}
	if len(job.Errors) > 0 {
		msgs := make([]string, 0, len(job.Errors))
		for _, e := range job.Errors {
			if e.Message != "" {
				msgs = append(msgs, e.Message)
			}
		}
		status.ErrorMessage = strings.Join(msgs, "; ")
	}
	return status, nil
}

// Retrieve fetches the results of a completed Mistral batch.
func (m *MistralBatchProvider) Retrieve(ctx context.Context, providerBatchID string) ([]BatchResponseItem, error) {
	job, err := m.getJob(ctx, providerBatchID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve batch job: %w", err)
	}

	state := mapMistralState(job.Status)
	if state != BatchStateCompleted {
		return nil, fmt.Errorf("batch is not completed (state: %s)", job.Status)
	}
	if job.OutputFile == nil || *job.OutputFile == "" {
		return nil, fmt.Errorf("no output_file on completed batch %s", providerBatchID)
	}

	outputBytes, err := m.downloadFile(ctx, *job.OutputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to download output file %s: %w", *job.OutputFile, err)
	}

	items, err := parseMistralOutputJSONL(outputBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse output jsonl: %w", err)
	}

	// Merge error_file entries if present. Non-fatal if the fetch fails.
	if job.ErrorFile != nil && *job.ErrorFile != "" {
		errBytes, errErr := m.downloadFile(ctx, *job.ErrorFile)
		if errErr != nil {
			log.Warn().Err(errErr).Str("error_file", *job.ErrorFile).Msg("Mistral batch: failed to download error file")
		} else {
			errItems, parseErr := parseMistralOutputJSONL(errBytes)
			if parseErr != nil {
				log.Warn().Err(parseErr).Msg("Mistral batch: failed to parse error file jsonl")
			} else {
				byID := make(map[string]int, len(items))
				for i, it := range items {
					byID[it.CustomID] = i
				}
				for _, ei := range errItems {
					if ei.Error == "" {
						continue
					}
					if idx, ok := byID[ei.CustomID]; ok {
						items[idx].Error = ei.Error
						items[idx].Response = nil
					} else {
						items = append(items, ei)
					}
				}
			}
		}
	}

	return items, nil
}

// buildInputJSONL serializes BatchRequests into Mistral's input JSONL format.
func (m *MistralBatchProvider) buildInputJSONL(requests []BatchRequest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for i, req := range requests {
		line := mistralInputLine{
			CustomID: req.CustomID,
			Body: mistralRequestBody{
				Model:          m.model,
				Messages:       req.Messages,
				Temperature:    req.Temperature,
				ResponseFormat: req.ResponseFormat,
			},
		}
		if err := enc.Encode(line); err != nil {
			return nil, fmt.Errorf("failed to encode input line %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

// uploadBatchFile POSTs the JSONL bytes to /v1/files with purpose=batch.
func (m *MistralBatchProvider) uploadBatchFile(ctx context.Context, jsonl []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("purpose", "batch"); err != nil {
		return "", fmt.Errorf("failed to write purpose field: %w", err)
	}

	filename := fmt.Sprintf("batch-%d.jsonl", time.Now().Unix())
	filePart, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("failed to create file form field: %w", err)
	}
	if _, err := filePart.Write(jsonl); err != nil {
		return "", fmt.Errorf("failed to write file bytes: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/v1/files", &body)
	if err != nil {
		return "", fmt.Errorf("failed to create upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("file upload HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(truncateBytes(respBody, 500))
		if OnCreditError != nil && isCreditError(resp.StatusCode, bodyStr) {
			OnCreditError("mistral", bodyStr)
		}
		return "", fmt.Errorf("file upload returned status %d: %s", resp.StatusCode, bodyStr)
	}

	var fileResp mistralFileUploadResponse
	if err := json.Unmarshal(respBody, &fileResp); err != nil {
		return "", fmt.Errorf("failed to parse upload response: %w (body: %s)", err, truncateBytes(respBody, 500))
	}
	if fileResp.ID == "" {
		return "", fmt.Errorf("upload response missing file id (body: %s)", truncateBytes(respBody, 500))
	}
	return fileResp.ID, nil
}

// getJob fetches a batch job by id.
func (m *MistralBatchProvider) getJob(ctx context.Context, jobID string) (*mistralBatchJob, error) {
	respBody, err := m.doJSONRequest(ctx, "GET", m.baseURL+"/v1/batch/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	var job mistralBatchJob
	if err := json.Unmarshal(respBody, &job); err != nil {
		return nil, fmt.Errorf("failed to parse batch job: %w (body: %s)", err, truncateBytes(respBody, 500))
	}
	return &job, nil
}

// downloadFile fetches raw bytes from /v1/files/{id}/content.
func (m *MistralBatchProvider) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	return m.doRawRequest(ctx, "GET", m.baseURL+"/v1/files/"+fileID+"/content")
}

// doJSONRequest performs a JSON HTTP request to the Mistral API.
func (m *MistralBatchProvider) doJSONRequest(ctx context.Context, method, url string, body any) ([]byte, error) {
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
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.httpClient.Do(req)
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
			OnCreditError("mistral", bodyStr)
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, bodyStr)
	}
	return respBody, nil
}

// doRawRequest performs a GET and returns raw bytes (for JSONL file downloads).
func (m *MistralBatchProvider) doRawRequest(ctx context.Context, method, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.httpClient.Do(req)
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
			OnCreditError("mistral", bodyStr)
		}
		return nil, fmt.Errorf("file download returned status %d: %s", resp.StatusCode, bodyStr)
	}
	return respBody, nil
}

// parseMistralOutputJSONL converts a JSONL body into BatchResponseItems.
func parseMistralOutputJSONL(data []byte) ([]BatchResponseItem, error) {
	lines := bytes.Split(data, []byte("\n"))
	items := make([]BatchResponseItem, 0, len(lines))
	for i, raw := range lines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var out mistralOutputLine
		if err := json.Unmarshal(line, &out); err != nil {
			return nil, fmt.Errorf("invalid jsonl at line %d: %w", i+1, err)
		}
		item := BatchResponseItem{CustomID: out.CustomID}
		switch {
		case out.Error != nil && out.Error.Message != "":
			item.Error = out.Error.Message
		case out.Response != nil && out.Response.StatusCode >= 200 && out.Response.StatusCode < 300:
			body := out.Response.Body
			item.Response = &body
		case out.Response != nil:
			item.Error = fmt.Sprintf("mistral request failed with status %d", out.Response.StatusCode)
		default:
			item.Error = "empty response from mistral"
		}
		items = append(items, item)
	}
	return items, nil
}

// mapMistralState maps Mistral batch job statuses to BatchState.
func mapMistralState(s string) BatchState {
	switch s {
	case "QUEUED":
		return BatchStatePending
	case "RUNNING", "CANCELLATION_REQUESTED":
		return BatchStateInProgress
	case "SUCCESS":
		return BatchStateCompleted
	case "FAILED", "CANCELLED":
		return BatchStateFailed
	case "TIMEOUT_EXCEEDED":
		return BatchStateExpired
	default:
		log.Warn().Str("state", s).Msg("Unknown Mistral batch state")
		return BatchStatePending
	}
}

// --- Mistral API types ---

type mistralInputLine struct {
	CustomID string             `json:"custom_id"`
	Body     mistralRequestBody `json:"body"`
}

type mistralRequestBody struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type mistralFileUploadResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Purpose  string `json:"purpose"`
}

type mistralBatchCreateRequest struct {
	InputFiles []string          `json:"input_files"`
	Endpoint   string            `json:"endpoint"`
	Model      string            `json:"model"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type mistralBatchJob struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	InputFiles        []string           `json:"input_files,omitempty"`
	OutputFile        *string            `json:"output_file,omitempty"`
	ErrorFile         *string            `json:"error_file,omitempty"`
	Endpoint          string             `json:"endpoint,omitempty"`
	Model             string             `json:"model,omitempty"`
	TotalRequests     int                `json:"total_requests,omitempty"`
	CompletedRequests int                `json:"completed_requests,omitempty"`
	SucceededRequests int                `json:"succeeded_requests,omitempty"`
	FailedRequests    int                `json:"failed_requests,omitempty"`
	Errors            []mistralBatchErr  `json:"errors,omitempty"`
	Metadata          map[string]string  `json:"metadata,omitempty"`
}

type mistralBatchErr struct {
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

type mistralOutputLine struct {
	CustomID string                `json:"custom_id"`
	Response *mistralOutputResp    `json:"response,omitempty"`
	Error    *mistralOutputErrBody `json:"error,omitempty"`
}

type mistralOutputResp struct {
	StatusCode int          `json:"status_code"`
	Body       ChatResponse `json:"body"`
}

type mistralOutputErrBody struct {
	Message string `json:"message"`
}
