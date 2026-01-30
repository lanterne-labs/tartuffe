package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// LLMRequestsTotal counts LLM API calls by type, model, and status
	LLMRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "llm_requests_total",
			Help:      "Total number of LLM API requests by type (chat/embedding), model, and status",
		},
		[]string{"type", "model", "status"},
	)

	// LLMRequestDuration tracks LLM API latency
	// Note: LLM calls can be very long (30-120s), so we use wider buckets
	LLMRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "llm_request_duration_seconds",
			Help:      "LLM API request latency in seconds",
			Buckets:   []float64{0.5, 1, 2.5, 5, 10, 15, 30, 45, 60, 90, 120, 180},
		},
		[]string{"type", "model"},
	)

	// LLMRetriesTotal counts retry attempts by reason
	LLMRetriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "llm_retries_total",
			Help:      "Total number of LLM API retry attempts by reason",
		},
		[]string{"type", "reason"},
	)

	// LLMEmbeddingBatchSize tracks the number of texts per embedding call
	LLMEmbeddingBatchSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "llm_embedding_batch_size",
			Help:      "Number of texts per embedding API call",
			Buckets:   []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000},
		},
	)
)

// ObserveLLMRequest records an LLM request completion
func ObserveLLMRequest(requestType, model string, duration time.Duration, err error) {
	status := "success"
	if err != nil {
		status = "error"
	}

	LLMRequestsTotal.WithLabelValues(requestType, model, status).Inc()
	LLMRequestDuration.WithLabelValues(requestType, model).Observe(duration.Seconds())
}

// IncrementLLMRetry increments the retry counter
func IncrementLLMRetry(requestType, reason string) {
	LLMRetriesTotal.WithLabelValues(requestType, reason).Inc()
}

// ObserveEmbeddingBatchSize records the batch size for an embedding request
func ObserveEmbeddingBatchSize(size int) {
	LLMEmbeddingBatchSize.Observe(float64(size))
}

// LLMTimer is a helper for timing LLM operations
type LLMTimer struct {
	requestType string
	model       string
	start       time.Time
}

// NewLLMTimer creates a new LLM timer
func NewLLMTimer(requestType, model string) *LLMTimer {
	return &LLMTimer{
		requestType: requestType,
		model:       model,
		start:       time.Now(),
	}
}

// ObserveDuration records the elapsed time and request status
func (lt *LLMTimer) ObserveDuration(err error) {
	ObserveLLMRequest(lt.requestType, lt.model, time.Since(lt.start), err)
}
