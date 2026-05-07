package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	// FactsWithoutStance is a backstop for the "every fact has a stance"
	// invariant enforced at insertion time by core.FactInserter. A non-zero
	// value means a fact landed in the database without going through the
	// stance pipeline — investigate immediately.
	FactsWithoutStance = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "facts_without_stance_total",
			Help:      "Number of facts not linked to any stance_documents row. Should always be 0 once stance backfill has run.",
		},
	)
)

// StartStanceInvariantCollector polls the database periodically and updates
// FactsWithoutStance. The query is cheap (NOT EXISTS over an index), so a
// minute-scale interval is fine.
func StartStanceInvariantCollector(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run once immediately so the gauge is populated before the first tick.
		collectStanceInvariant(ctx, pool)

		for {
			select {
			case <-ctx.Done():
				log.Debug().Msg("Stopping stance invariant collector")
				return
			case <-ticker.C:
				collectStanceInvariant(ctx, pool)
			}
		}
	}()
}

func collectStanceInvariant(ctx context.Context, pool *pgxpool.Pool) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Excludes facts marked as duplicates of another fact via
	// fact_relations.relation_type = 'duplicate' — those are pointers to a
	// canonical fact and do not get their own stance link by design.
	var count int64
	err := pool.QueryRow(queryCtx, `
		SELECT COUNT(*)
		FROM facts f
		WHERE NOT EXISTS (
			SELECT 1 FROM stance_document_facts sdf WHERE sdf.fact_id = f.id
		)
		AND NOT EXISTS (
			SELECT 1 FROM fact_relations fr
			WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)`).Scan(&count)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to compute facts_without_stance gauge")
		return
	}

	FactsWithoutStance.Set(float64(count))
	if count > 0 {
		log.Warn().
			Int64("count", count).
			Msg("Facts without a stance detected — invariant violation; run `dm facts assign-stances`")
	}
}
