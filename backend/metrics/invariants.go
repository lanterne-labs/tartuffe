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

	// StanceRoutingsPending is the per-(person, fact) view of the same
	// invariant: mention pairs whose fact is not yet in that person's stances.
	// Non-zero is NORMAL while the nightly stance fan-out drains them; it
	// should trend to ~0 between nights, not stay flat.
	StanceRoutingsPending = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "stance_routings_pending_total",
			Help:      "Number of (person, fact) mention pairs not yet routed into that person's stance documents (the stance fan-out backlog).",
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
			Msg("Facts without a stance detected — invariant violation; run `dm facts assign-stances` (a fact with a NULL embedding is invisible to it — regenerate its embedding first)")
	}

	// Per-(person, fact) backlog: same definition as the fan-out's pending set
	// (person-scoped orphan queries). Non-zero while the nightly fan-out drains.
	var pending int64
	err = pool.QueryRow(queryCtx, `
		SELECT COUNT(*) FROM (
			SELECT DISTINCT fm.person_id, fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			JOIN facts f ON f.id = fs.fact_id
			WHERE f.embedding IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			)
			AND NOT EXISTS (
				SELECT 1 FROM stance_document_facts sdf
				JOIN stance_documents sd ON sd.id = sdf.stance_document_id
				WHERE sdf.fact_id = f.id AND sd.person_id = fm.person_id
			)
		) p`).Scan(&pending)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to compute stance_routings_pending gauge")
		return
	}
	StanceRoutingsPending.Set(float64(pending))
}
