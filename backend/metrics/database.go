package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	// DBQueryDuration tracks database query latency by operation type
	DBQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "db_query_duration_seconds",
			Help:      "Database query latency in seconds by operation type",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"operation"},
	)

	// DBQueriesTotal counts database queries by operation and status
	DBQueriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "db_queries_total",
			Help:      "Total number of database queries by operation and status",
		},
		[]string{"operation", "status"},
	)

	// DBConnectionPoolTotal tracks connection pool state
	DBConnectionPoolTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "db_connection_pool_total",
			Help:      "Database connection pool state",
		},
		[]string{"state"},
	)

	// DBTransactionDuration tracks transaction duration
	DBTransactionDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "db_transaction_duration_seconds",
			Help:      "Database transaction duration in seconds",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
	)

	// DBVectorSearchDuration tracks pgvector K-NN search latency
	DBVectorSearchDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "db_vector_search_duration_seconds",
			Help:      "pgvector K-NN search latency in seconds",
			Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
	)

	// DBVectorSearchResults tracks K-NN result count distribution
	DBVectorSearchResults = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "db_vector_search_results",
			Help:      "Number of results from pgvector K-NN searches",
			Buckets:   []float64{0, 1, 5, 10, 25, 50, 100, 250, 500},
		},
	)
)

// ObserveQueryDuration records query duration for a specific operation
func ObserveQueryDuration(operation string, duration time.Duration) {
	DBQueryDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// IncrementQueryTotal increments the query counter for an operation
func IncrementQueryTotal(operation, status string) {
	DBQueriesTotal.WithLabelValues(operation, status).Inc()
}

// ObserveVectorSearch records vector search metrics
func ObserveVectorSearch(duration time.Duration, resultCount int) {
	DBVectorSearchDuration.Observe(duration.Seconds())
	DBVectorSearchResults.Observe(float64(resultCount))
}

// ObserveTransactionDuration records transaction duration
func ObserveTransactionDuration(duration time.Duration) {
	DBTransactionDuration.Observe(duration.Seconds())
}

// StartPoolStatsCollector starts a goroutine that collects pool stats periodically
func StartPoolStatsCollector(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Debug().Msg("Stopping pool stats collector")
				return
			case <-ticker.C:
				collectPoolStats(pool)
			}
		}
	}()
}

// collectPoolStats updates pool metrics
func collectPoolStats(pool *pgxpool.Pool) {
	stats := pool.Stat()

	DBConnectionPoolTotal.WithLabelValues("acquired").Set(float64(stats.AcquiredConns()))
	DBConnectionPoolTotal.WithLabelValues("idle").Set(float64(stats.IdleConns()))
	DBConnectionPoolTotal.WithLabelValues("total").Set(float64(stats.TotalConns()))
	DBConnectionPoolTotal.WithLabelValues("max").Set(float64(stats.MaxConns()))
	DBConnectionPoolTotal.WithLabelValues("constructing").Set(float64(stats.ConstructingConns()))
}

// QueryTimer is a helper for timing database queries
type QueryTimer struct {
	operation string
	start     time.Time
}

// NewQueryTimer creates a new query timer for the given operation
func NewQueryTimer(operation string) *QueryTimer {
	return &QueryTimer{
		operation: operation,
		start:     time.Now(),
	}
}

// ObserveDuration records the elapsed time and increments the counter
func (qt *QueryTimer) ObserveDuration(err error) {
	duration := time.Since(qt.start)
	ObserveQueryDuration(qt.operation, duration)

	status := "success"
	if err != nil {
		status = "error"
	}
	IncrementQueryTotal(qt.operation, status)
}
