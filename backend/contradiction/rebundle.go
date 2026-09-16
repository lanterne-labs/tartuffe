package contradiction

import (
	"context"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// RebundlePerson re-runs the two-phase bundling logic for a single person.
// Phase 1 is the SQL fast path (free) that pins bundle_stance_id on
// contradictions whose two facts now share a stance. Phase 2 is the LLM
// Bundler over the remaining solos (true cross-stance contradictions).
// Returns (rebundled_via_sql, rebundled_via_llm, left_solo_after_llm). All
// errors are logged and counted as failures rather than aborting the
// caller — bundling is recoverable later, and a single person's failure
// should not block a batch run.
//
// Shared between `dm stances rebuild` (post-rebuild rebundle) and
// `dm facts assign-stances` (post-orphan-assignment rebundle).
func RebundlePerson(
	ctx context.Context,
	storer *storage.PostgresStorer,
	bundler *Bundler,
	p storage.PersonInfo,
) (fastPath, llmPath, failed int64) {
	pid := p.ID

	updated, err := storer.BundleBackfillFastPath(ctx, &pid)
	if err != nil {
		log.Error().Err(err).Str("person", p.Name).Msg("Bundle phase 1 (SQL fast path) failed")
		return 0, 0, 0
	}

	solos, err := storer.ListSoloContradictionsForBundling(ctx, &pid, 0)
	if err != nil {
		log.Error().Err(err).Str("person", p.Name).Msg("Bundle phase 2 prep: list solos failed")
		return updated, 0, 0
	}
	if len(solos) == 0 {
		return updated, 0, 0
	}

	for _, row := range solos {
		if err := bundler.Resolve(ctx, row.PersonID, row.ID, row.FactAID, row.FactBID, row.FactAText, row.FactBText, row.Reason); err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int64("contradiction_id", row.ID).Msg("Bundle phase 2 (LLM) failed; row stays solo")
			failed++
			continue
		}
		llmPath++
	}

	if updated > 0 || llmPath > 0 || failed > 0 {
		log.Info().
			Str("person", p.Name).
			Int64("rebundled_sql", updated).
			Int64("rebundled_llm", llmPath).
			Int64("left_solo", failed).
			Msg("Rebundled contradictions for person")
	}
	return updated, llmPath, failed
}

// RebundleAll runs the SQL fast path DB-wide (no person filter), then iterates
// every remaining solo contradiction in the database and asks the LLM Bundler
// to pick a home stance. Anything still solo after this is a true cross-stance
// case the Bundler explicitly declined.
//
// Returns (rebundled_via_sql, rebundled_via_llm, declined_or_failed).
func RebundleAll(
	ctx context.Context,
	storer *storage.PostgresStorer,
	bundler *Bundler,
) (fastPath, llmPath, failed int64) {
	updated, err := storer.BundleBackfillFastPath(ctx, nil)
	if err != nil {
		log.Error().Err(err).Msg("DB-wide bundle phase 1 (SQL fast path) failed")
		return 0, 0, 0
	}

	solos, err := storer.ListSoloContradictionsForBundling(ctx, nil, 0)
	if err != nil {
		log.Error().Err(err).Msg("DB-wide bundle phase 2 prep: list solos failed")
		return updated, 0, 0
	}
	log.Info().Int64("sql_updated", updated).Int("remaining_solos", len(solos)).Msg("DB-wide rebundle: phase 1 done, starting LLM bundler")

	for _, row := range solos {
		if err := bundler.Resolve(ctx, row.PersonID, row.ID, row.FactAID, row.FactBID, row.FactAText, row.FactBText, row.Reason); err != nil {
			log.Warn().Err(err).Int64("contradiction_id", row.ID).Int64("person_id", row.PersonID).Msg("Bundle phase 2 (LLM) failed; row stays solo")
			failed++
			continue
		}
		llmPath++
	}

	log.Info().
		Int64("rebundled_sql", updated).
		Int64("rebundled_llm", llmPath).
		Int64("left_solo", failed).
		Msg("DB-wide rebundle complete")
	return updated, llmPath, failed
}
