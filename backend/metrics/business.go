package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// FactsExtractedTotal counts facts extracted by source type and canonical status
	FactsExtractedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "facts_extracted_total",
			Help:      "Total number of facts extracted by source type and canonical status",
		},
		[]string{"source_type", "is_canonical"},
	)

	// ContradictionsDetectedTotal counts total contradictions found
	ContradictionsDetectedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "contradictions_detected_total",
			Help:      "Total number of contradictions detected",
		},
	)

	// ContradictionChecksTotal counts contradiction checks by result
	ContradictionChecksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "contradiction_checks_total",
			Help:      "Total number of contradiction checks by result (found/not_found/error)",
		},
		[]string{"result"},
	)

	// NewsArticlesProcessedTotal counts news articles by status
	NewsArticlesProcessedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "news_articles_processed_total",
			Help:      "Total number of news articles processed by status (parsed/parsed_no_facts/skipped/error)",
		},
		[]string{"status"},
	)

	// FactsSkippedDedupTotal counts facts skipped as duplicates at insertion time
	FactsSkippedDedupTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "facts_skipped_dedup_total",
			Help:      "Total number of facts skipped as duplicates at insertion time",
		},
	)

	// ContentExtractionDuration tracks content scraping latency by extractor
	ContentExtractionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "content_extraction_duration_seconds",
			Help:      "Content extraction latency in seconds by extractor type",
			Buckets:   []float64{0.5, 1, 2.5, 5, 10, 15, 30, 45, 60},
		},
		[]string{"extractor"},
	)

	// TweetsProcessedTotal counts tweets by status
	TweetsProcessedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "tweets_processed_total",
			Help:      "Total number of tweets processed by status (parsed/parsed_no_facts/fact_extracted/skipped_retweet/skipped_already_processed/error)",
		},
		[]string{"status"},
	)

	// HonestyScoreUpdates tracks the distribution of honesty score updates
	HonestyScoreUpdates = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "honesty_score_updates",
			Help:      "Distribution of honesty score values when updated",
			Buckets:   []float64{0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
	)

	// ContradictionAuditsTotal counts contradiction auditor verdicts emitted
	// by the periodic auditor pass. Labels: verdict ∈ {keep, remove, ambiguous, error}.
	// "remove" is the actionable one — non-zero means the auditor flagged false
	// positives that someone (a human or `dm contradictions check --apply`)
	// should clean up.
	ContradictionAuditsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "contradiction_audits_total",
			Help:      "Auditor verdicts on already-recorded contradictions, by verdict (keep/remove/ambiguous/error)",
		},
		[]string{"verdict"},
	)

	// ContradictionAuditRunsTotal counts how many times the periodic auditor
	// has run. Used to alert on staleness — if the daily pipeline is firing
	// but this counter hasn't moved in >6 days, the cadence gate is wedged.
	ContradictionAuditRunsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "contradiction_audit_runs_total",
			Help:      "Total number of contradiction-auditor passes that have run to completion",
		},
	)
)

// IncrementFactsExtracted increments the fact counter
func IncrementFactsExtracted(sourceType string, isCanonical bool) {
	canonical := "false"
	if isCanonical {
		canonical = "true"
	}
	FactsExtractedTotal.WithLabelValues(sourceType, canonical).Inc()
}

// IncrementContradictionsDetected increments the contradiction counter
func IncrementContradictionsDetected() {
	ContradictionsDetectedTotal.Inc()
}

// IncrementContradictionChecks increments the contradiction check counter
func IncrementContradictionChecks(result string) {
	ContradictionChecksTotal.WithLabelValues(result).Inc()
}

// IncrementNewsArticles increments the news article counter
func IncrementNewsArticles(status string) {
	NewsArticlesProcessedTotal.WithLabelValues(status).Inc()
}

// IncrementTweets increments the tweet counter
func IncrementTweets(status string) {
	TweetsProcessedTotal.WithLabelValues(status).Inc()
}

// IncrementFactsSkippedDedup increments the dedup skip counter by n
func IncrementFactsSkippedDedup(n int) {
	FactsSkippedDedupTotal.Add(float64(n))
}

// ObserveHonestyScore records an honesty score update
func ObserveHonestyScore(score float64) {
	HonestyScoreUpdates.Observe(score)
}

// IncrementContradictionAudits bumps the auditor verdict counter.
func IncrementContradictionAudits(verdict string) {
	ContradictionAuditsTotal.WithLabelValues(verdict).Inc()
}

// IncrementContradictionAuditRuns bumps the auditor-pass counter (once per
// completed periodic run).
func IncrementContradictionAuditRuns() {
	ContradictionAuditRunsTotal.Inc()
}
