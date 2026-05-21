// Package metrics provides Prometheus metrics for the Digital Mirror application.
// It includes HTTP, database, LLM, and business metrics with a custom registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const (
	// Namespace is the prefix for all Digital Mirror metrics
	Namespace = "digital_mirror"
)

var (
	// Registry is the custom Prometheus registry for all Digital Mirror metrics
	Registry = prometheus.NewRegistry()
)

func init() {
	// Register standard Go and process collectors
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Register HTTP metrics
	Registry.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		HTTPRequestsInFlight,
		SSEConnectionsActive,
	)

	// Register database metrics
	Registry.MustRegister(
		DBQueryDuration,
		DBQueriesTotal,
		DBConnectionPoolTotal,
		DBTransactionDuration,
		DBVectorSearchDuration,
		DBVectorSearchResults,
	)

	// Register LLM metrics
	Registry.MustRegister(
		LLMRequestsTotal,
		LLMRequestDuration,
		LLMRetriesTotal,
		LLMEmbeddingBatchSize,
	)

	// Register business metrics
	Registry.MustRegister(
		FactsExtractedTotal,
		FactsSkippedDedupTotal,
		ContradictionsDetectedTotal,
		ContradictionChecksTotal,
		ContradictionAuditsTotal,
		ContradictionAuditRunsTotal,
		NewsArticlesProcessedTotal,
		TweetsProcessedTotal,
		ContentExtractionDuration,
		HonestyScoreUpdates,
	)

	// Register invariant gauges
	Registry.MustRegister(
		FactsWithoutStance,
	)
}
