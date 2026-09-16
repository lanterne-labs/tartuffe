package core

import (
	"context"
	"fmt"
	"sort"

	"github.com/rs/zerolog/log"
)

// StanceRoutingFact is a fact in the shape the stance router (ProcessNewFact)
// needs: text, topic slug, verb and embedding.
type StanceRoutingFact struct {
	ID            int64
	FactText      string
	EmbeddingText string
	CanonicalVerb string
	Embedding     []float32
}

// StanceBackfillStorer is the storage view the backfill / rebuild paths need.
// storage.PostgresStorer implements it.
type StanceBackfillStorer interface {
	StanceMergeStorer
	// GetFactsForStanceBackfill returns every non-duplicate fact mentioning
	// the person that carries an embedding, oldest id first.
	GetFactsForStanceBackfill(ctx context.Context, personID int64) ([]StanceRoutingFact, error)
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error
}

// PersonRebundler re-runs contradiction bundling for one person after its
// stances changed, returning (rebundled_via_sql, rebundled_via_llm,
// left_solo). It is implemented outside core: the bundler lives in
// backend/contradiction, which core cannot import.
type PersonRebundler func(ctx context.Context, p PersonRecord) (fast, llm, failed int64)

// BackfillGroupPreview is one exact-label group a dry-run backfill would route.
type BackfillGroupPreview struct {
	Label            string
	FactCount        int
	RepresentativeID int64
}

// BackfillStats accumulates per-person counters for the rebuild/backfill summary.
type BackfillStats struct {
	StancesCreated          int
	StancesMerged           int
	ContradictionsDetected  int // detected during per-fact ProcessNewFact
	PostMergeContradictions int // detected during post-merge re-analysis (cross-stance pairs)
	// DryRunGroups holds, in dry-run mode only, the groups that would have
	// been routed, for the caller to display.
	DryRunGroups []BackfillGroupPreview
}

// BackfillStancesForPerson assigns each of the person's facts to a stance
// (creating stances as needed) through the same per-fact LLM topic-assignment
// path as production ingest, then runs the LLM-vetted merge sweep the daily
// pipeline uses. Facts sharing an exact embedding_text share one ProcessNewFact
// call (representative + cheap join inserts for the duplicates). A nil
// stanceManager behaves like dryRun.
func BackfillStancesForPerson(
	ctx context.Context,
	storer StanceBackfillStorer,
	stanceManager *StanceManager,
	p PersonRecord,
	dryRun bool,
) (BackfillStats, error) {
	var stats BackfillStats

	facts, err := storer.GetFactsForStanceBackfill(ctx, p.ID)
	if err != nil {
		return stats, fmt.Errorf("loading facts: %w", err)
	}

	if len(facts) == 0 {
		log.Info().Str("person", p.Name).Msg("No facts with embeddings found, skipping")
		return stats, nil
	}

	log.Info().Str("person", p.Name).Int("facts", len(facts)).Msg("Loaded facts for per-fact stance assignment")

	// Group facts by exact embedding_text so identical labels share one LLM call.
	// The first fact in each group is the representative passed to ProcessNewFact;
	// the rest are joined to the assigned stance via cheap ON CONFLICT inserts.
	groups := clusterFactsByExactLabel(facts)

	// Deterministic order: process groups by the smallest fact ID inside them so
	// semantically related facts surfaced earlier (e.g. clustered by ingest time)
	// have a chance to seed the topic before later contradictions arrive.
	type groupEntry struct {
		label string
		facts []StanceRoutingFact
		minID int64
	}
	entries := make([]groupEntry, 0, len(groups))
	for label, gFacts := range groups {
		minID := gFacts[0].ID
		for _, f := range gFacts[1:] {
			if f.ID < minID {
				minID = f.ID
			}
		}
		entries = append(entries, groupEntry{label: label, facts: gFacts, minID: minID})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].minID < entries[j].minID })

	if dryRun || stanceManager == nil {
		for _, e := range entries {
			stats.DryRunGroups = append(stats.DryRunGroups, BackfillGroupPreview{Label: e.label, FactCount: len(e.facts), RepresentativeID: e.facts[0].ID})
			stats.StancesCreated++
		}
		return stats, nil
	}

	// Defer the per-fact refresh — refresh each touched stance ONCE
	// after the loop instead of after every NEW_INFO/CONTRADICTION verdict.
	// For a heavy person (e.g. 40 facts in one stance) this turns 40 refresh
	// LLM calls into 1. The final state is identical because the last refresh
	// folds every fact added since the stance's last refresh (a full rebuild when
	// they fit the context).
	ctxDefer := WithDeferRefresh(ctx)
	// Track which stances need an end-of-loop refresh. Only NEW_INFO and
	// CONTRADICTION verdicts would have triggered an inline refresh in the
	// non-deferred path — match that selectively so we don't refresh stances
	// whose only verdicts were NEW_STANCE or DUPLICATE (label is already
	// authoritative).
	stancesNeedingRefresh := make(map[int64]struct{})

	for _, e := range entries {
		rep := e.facts[0]
		res, err := stanceManager.ProcessNewFact(ctxDefer, p.ID, rep.ID, rep.FactText, rep.Embedding, rep.CanonicalVerb)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Int64("fact_id", rep.ID).Msg("ProcessNewFact failed; skipping group")
			continue
		}
		if res.Verdict == "NEW_STANCE" {
			stats.StancesCreated++
		}
		if res.Verdict == "CONTRADICTION" {
			stats.ContradictionsDetected++
		}
		if res.Verdict == "NEW_INFO" || res.Verdict == "CONTRADICTION" {
			stancesNeedingRefresh[res.StanceID] = struct{}{}
		}

		// Attach remaining facts in the exact-label group directly to the assigned
		// stance. We deliberately skip the LLM-analyse path for these — the rep has
		// already established the verdict for this label, and AddFactToStance is
		// idempotent via ON CONFLICT.
		for _, f := range e.facts[1:] {
			if err := storer.AddFactToStance(ctx, res.StanceID, f.ID); err != nil {
				log.Warn().Err(err).Int64("fact_id", f.ID).Int64("stance_id", res.StanceID).Msg("Failed to attach exact-label dup to stance")
			}
		}
	}

	// Now refresh each touched stance ONCE (we deferred during the loop). For
	// stances that only saw NEW_STANCE / DUPLICATE verdicts this is a no-op
	// per the verdict guard inside handleVerdict, but RefreshStanceMetadata
	// is unconditional — so we only refresh stances that got a NEW_INFO or
	// CONTRADICTION verdict (tracked via stancesNeedingRefresh).
	for stanceID := range stancesNeedingRefresh {
		if err := stanceManager.RefreshStanceMetadata(ctx, stanceID); err != nil {
			log.Warn().Err(err).Int64("stance_id", stanceID).Str("person", p.Name).Msg("Backfill: end-of-loop refresh failed; stance keeps previous label/summary")
		}
	}

	// LLM-vetted merge sweep — same code path the daily pipeline + `dm stances
	// merge` use. Phase A agglomerative clustering of centroids, Phase B
	// per-cluster LLM verification, then post-merge contradiction re-detection
	// across previously-fragmented facts.
	ps, err := MergeStancesForPerson(ctx, storer, stanceManager, p, DefaultStanceMergeOptions)
	if err != nil {
		log.Error().Err(err).Str("person", p.Name).Msg("LLM-vetted merge phase failed (stances kept as-is)")
	} else {
		stats.StancesMerged = int(ps.StancesMerged)
		stats.PostMergeContradictions = int(ps.PostMergeContradictions)
	}

	log.Info().
		Str("person", p.Name).
		Int("stances_created", stats.StancesCreated).
		Int("stances_merged", stats.StancesMerged).
		Int("contradictions_detected", stats.ContradictionsDetected).
		Int("post_merge_contradictions", stats.PostMergeContradictions).
		Msg("Backfill done for person")

	return stats, nil
}

// clusterFactsByExactLabel groups facts by exact embedding_text match. A fact
// without embedding_text forms its own group.
func clusterFactsByExactLabel(facts []StanceRoutingFact) map[string][]StanceRoutingFact {
	clusters := make(map[string][]StanceRoutingFact)
	for _, fact := range facts {
		label := fact.EmbeddingText
		if label == "" {
			label = fmt.Sprintf("unnamed-fact-%d", fact.ID)
		}
		clusters[label] = append(clusters[label], fact)
	}
	return clusters
}

// RebuildOptions tunes RebuildStancesForPerson.
type RebuildOptions struct {
	DryRun bool
	// Resume skips persons that already have at least one stance (for
	// resuming an interrupted rebuild).
	Resume bool
}

// RebuildStats are the counters of one person's rebuild.
type RebuildStats struct {
	Skipped       bool // Resume: the person already had stances
	Deleted       int  // existing stances deleted (in dry-run: that would have been)
	Backfill      BackfillStats
	RebundledFast int64
	RebundledLLM  int64
	BundleFailed  int64
}

// RebuildStancesForPerson deletes the person's stance documents, rebuilds them
// from the facts (BackfillStancesForPerson), then rebundles the contradictions
// whose bundle_stance_id was wiped by the FK ON DELETE SET NULL when the
// stances were dropped — without it the person would be left with correct
// stances and all contradictions solo. On error the returned stats still
// carry what already happened (Deleted).
func RebuildStancesForPerson(
	ctx context.Context,
	storer StanceBackfillStorer,
	stanceManager *StanceManager,
	rebundle PersonRebundler,
	p PersonRecord,
	opts RebuildOptions,
) (RebuildStats, error) {
	var st RebuildStats

	existing, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
	if err != nil {
		return st, fmt.Errorf("loading existing stances: %w", err)
	}
	if opts.Resume && len(existing) > 0 {
		st.Skipped = true
		return st, nil
	}

	if len(existing) > 0 {
		if !opts.DryRun {
			if err := storer.DeleteStanceDocumentsForPerson(ctx, p.ID); err != nil {
				return st, fmt.Errorf("deleting existing stances: %w", err)
			}
			log.Info().Str("person", p.Name).Int("deleted", len(existing)).Msg("Deleted existing stances")
		}
		st.Deleted = len(existing)
	}

	st.Backfill, err = BackfillStancesForPerson(ctx, storer, stanceManager, p, opts.DryRun)
	if err != nil {
		return st, err
	}
	if opts.DryRun {
		return st, nil
	}

	st.RebundledFast, st.RebundledLLM, st.BundleFailed = rebundle(ctx, p)
	return st, nil
}
