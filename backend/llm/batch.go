package llm

import "context"

// BatchState represents the state of a batch job.
type BatchState string

const (
	BatchStatePending    BatchState = "pending"
	BatchStateInProgress BatchState = "in_progress"
	BatchStateCompleted  BatchState = "completed"
	BatchStateFailed     BatchState = "failed"
	BatchStateExpired    BatchState = "expired"
)

// BatchRequest represents a single request within a batch.
type BatchRequest struct {
	CustomID       string        // "url-hash:chunk-index" for correlating results
	Messages       []ChatMessage // reuse existing type from client.go
	Temperature    float64
	ResponseFormat *ResponseFormat
}

// BatchSubmitResult is returned after a batch is submitted to the provider.
type BatchSubmitResult struct {
	ProviderBatchID string
	RequestCount    int
}

// BatchStatus represents the current state of a batch at the provider.
type BatchStatus struct {
	ProviderBatchID string
	State           BatchState
	CompletedCount  int
	FailedCount     int
	ErrorMessage    string
}

// BatchResponseItem represents a single response from a completed batch.
type BatchResponseItem struct {
	CustomID string
	Response *ChatResponse // nil if errored
	Error    string
}

// BatchProvider abstracts the batch API of an LLM provider.
// All three major providers (Gemini, Anthropic, OpenAI) follow the same pattern:
// submit requests -> poll status -> retrieve results.
type BatchProvider interface {
	// Submit sends a batch of requests to the provider.
	Submit(ctx context.Context, requests []BatchRequest) (*BatchSubmitResult, error)

	// Status checks the current state of a batch at the provider.
	Status(ctx context.Context, providerBatchID string) (*BatchStatus, error)

	// Retrieve fetches the results of a completed batch.
	Retrieve(ctx context.Context, providerBatchID string) ([]BatchResponseItem, error)

	// ProviderName returns the name of the provider (e.g., "gemini", "openai").
	ProviderName() string
}
