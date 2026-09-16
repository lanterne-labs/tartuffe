package core

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// StanceAssignStorer is the storage view the orphan-fact assignment and the
// nightly stance fan-out need. storage.PostgresStorer implements it.
type StanceAssignStorer interface {
	// ListPersonsWithOrphanFacts lists every person with at least one
	// non-duplicate mentioned fact not linked to one of THAT PERSON's stances.
	ListPersonsWithOrphanFacts(ctx context.Context) ([]PersonRecord, error)
	ListOrphanFactsForPerson(ctx context.Context, personID int64) ([]StanceRoutingFact, error)
	// CountPendingStanceRoutings counts the (person, fact) pairs still to route.
	CountPendingStanceRoutings(ctx context.Context) (int, error)
}

// StanceFanoutNightlyBudget caps how many (person, fact) stance routings the
// daily pipeline performs per night. Sized against the serialized local GPU:
// most routings for vote co-actors are a single topic-assignment call (new
// stance on a near-empty deputy), so 250 fits comfortably in the freed
// re-baseline window. The pending set is derived from the DB, so whatever is
// not processed tonight simply remains pending for tomorrow (self-draining,
// self-terminating once the backlog is gone).
const StanceFanoutNightlyBudget = 250

// PersonAssignStats are the counters of one person's orphan assignment.
type PersonAssignStats struct {
	Skipped              bool // orphans gone between listing and processing, or run budget exhausted
	OrphansAssigned      int64
	OrphanFailures       int64
	VerdictNewStance     int64
	VerdictDuplicate     int64
	VerdictNewInfo       int64
	VerdictContradiction int64
	RebundleSQL          int64
	RebundleLLM          int64
	RebundleSolosLeft    int64
}

// AssignOrphansForPerson routes every orphan fact of one person through
// ProcessNewFact, then rebundles that person's solo contradictions. budget,
// when non-nil, is a shared atomic countdown of (person, fact) routings across
// the whole run: each person reserves up to len(orphans) slots and processes
// only what it got — leftovers stay orphans and are picked up by the next run
// (the orphan queries derive the pending set from the DB). In dry-run mode
// nothing is written and the orphan count is only logged.
func AssignOrphansForPerson(
	ctx context.Context,
	storer StanceAssignStorer,
	stanceManager *StanceManager,
	rebundle PersonRebundler,
	p PersonRecord,
	dryRun bool,
	budget *int64,
) (PersonAssignStats, error) {
	var ps PersonAssignStats

	orphans, err := storer.ListOrphanFactsForPerson(ctx, p.ID)
	if err != nil {
		return ps, fmt.Errorf("loading orphans: %w", err)
	}
	if len(orphans) == 0 {
		ps.Skipped = true
		return ps, nil
	}
	if budget != nil {
		left := atomic.AddInt64(budget, -int64(len(orphans)))
		if left < 0 {
			allowed := int64(len(orphans)) + left
			if allowed <= 0 {
				ps.Skipped = true
				return ps, nil
			}
			orphans = orphans[:allowed]
		}
	}

	if dryRun {
		log.Info().Str("person", p.Name).Int("orphan_count", len(orphans)).Msg("[DRY-RUN] Would process orphan facts")
		return ps, nil
	}

	for _, f := range orphans {
		res, err := stanceManager.ProcessNewFact(ctx, p.ID, f.ID, f.FactText, f.Embedding, f.CanonicalVerb)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int64("fact_id", f.ID).Msg("ProcessNewFact failed; fact stays orphan")
			ps.OrphanFailures++
			continue
		}
		ps.OrphansAssigned++
		switch res.Verdict {
		case "NEW_STANCE":
			ps.VerdictNewStance++
		case "DUPLICATE":
			ps.VerdictDuplicate++
		case "NEW_INFO":
			ps.VerdictNewInfo++
		case "CONTRADICTION":
			ps.VerdictContradiction++
		}
	}

	ps.RebundleSQL, ps.RebundleLLM, ps.RebundleSolosLeft = rebundle(ctx, p)

	log.Info().
		Str("person", p.Name).
		Int("orphans", len(orphans)).
		Int64("assigned", ps.OrphansAssigned).
		Int64("failed", ps.OrphanFailures).
		Int64("rebundle_sql", ps.RebundleSQL).
		Int64("rebundle_llm", ps.RebundleLLM).
		Msg("assign-stances done for person")

	return ps, nil
}

// RunStanceFanout is the daily-pipeline stance fan-out step (mirror-py3):
// route pending (person, fact) mention pairs — mostly shared vote facts for
// their co-voters — through the live ProcessNewFact path, up to budget
// routings per run (StanceFanoutNightlyBudget for the nightly run). The
// pending set is derived from the DB by the person-scoped orphan queries, so
// the step is idempotent, retries failures on later nights, and goes silent
// once the backlog is drained and each night's new facts fit the budget.
func RunStanceFanout(ctx context.Context, storer StanceAssignStorer, stanceManager *StanceManager, rebundle PersonRebundler, budget int) error {
	persons, err := storer.ListPersonsWithOrphanFacts(ctx)
	if err != nil {
		return fmt.Errorf("stance fan-out: listing persons with orphan facts: %w", err)
	}
	if len(persons) == 0 {
		return nil
	}

	// Shuffle so one early-alphabet person with a large (or persistently
	// failing) orphan set cannot monopolize the nightly budget forever —
	// every person gets budget eventually regardless of name.
	rand.Shuffle(len(persons), func(i, j int) { persons[i], persons[j] = persons[j], persons[i] })

	remaining := int64(budget)
	var routed, failures, contradictions int64
	for _, p := range persons {
		if atomic.LoadInt64(&remaining) <= 0 {
			break
		}
		ps, err := AssignOrphansForPerson(ctx, storer, stanceManager, rebundle, p, false, &remaining)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Stance fan-out failed for person")
			continue
		}
		routed += ps.OrphansAssigned
		failures += ps.OrphanFailures
		contradictions += ps.VerdictContradiction
	}

	pending, cErr := storer.CountPendingStanceRoutings(ctx)
	if cErr != nil {
		log.Warn().Err(cErr).Msg("Stance fan-out: pending count failed")
	}
	log.Info().
		Int64("routed", routed).
		Int64("failures", failures).
		Int64("contradictions", contradictions).
		Int("pending_remaining", pending).
		Msg("Daily pipeline: stance fan-out done")
	return nil
}
