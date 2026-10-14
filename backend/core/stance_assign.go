package core

import (
	"context"
	"digital-mirror/backend/llm"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// StanceAssignStorer is the storage view the orphan-fact assignment needs.
// storage.PostgresStorer implements it.
type StanceAssignStorer interface {
	// ListPersonsWithOrphanFacts lists every person with at least one
	// non-duplicate mentioned fact not linked to one of THAT PERSON's stances.
	ListPersonsWithOrphanFacts(ctx context.Context) ([]PersonRecord, error)
	ListOrphanFactsForPerson(ctx context.Context, personID int64) ([]StanceRoutingFact, error)
}

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
//
// A failed routing writes nothing (see ProcessNewFact), so the fact stays an
// orphan for the next run. When the LLM is unavailable the person stops at
// that fact and the error (wrapping llm.ErrUnavailable) is returned; so does
// a cancelled ctx, with an error wrapping ctx.Err().
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
		if err := ctx.Err(); err != nil {
			return ps, fmt.Errorf("stance assignment stopped: %w", err)
		}
		res, err := stanceManager.ProcessNewFact(ctx, p.ID, f.ID, f.FactText, f.Embedding, f.CanonicalVerb)
		if errors.Is(err, llm.ErrUnavailable) {
			return ps, fmt.Errorf("routing fact %d: %w", f.ID, err)
		}
		if err != nil && ctx.Err() != nil {
			// Cut by the cancelled run, not a failure of the fact.
			return ps, fmt.Errorf("routing fact %d stopped: %w", f.ID, ctx.Err())
		}
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int64("fact_id", f.ID).Msg("ProcessNewFact failed; fact stays orphan")
			ps.OrphanFailures++
			continue
		}
		ps.OrphansAssigned++
		switch res.Verdict {
		case VerdictNewStance:
			ps.VerdictNewStance++
		case VerdictDuplicate:
			ps.VerdictDuplicate++
		case VerdictNewInfo:
			ps.VerdictNewInfo++
		case VerdictContradiction:
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
