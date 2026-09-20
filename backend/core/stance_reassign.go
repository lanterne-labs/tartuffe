package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// StanceReassignStorer is the storage view the singleton re-assignment sweep
// needs. storage.PostgresStorer implements it.
type StanceReassignStorer interface {
	StanceMergeStorer
	// ListPersonsWithSingletonStances lists the persons with at least one
	// one-fact stance and at least one other stance it could join, search
	// queue members first.
	ListPersonsWithSingletonStances(ctx context.Context) ([]PersonRecord, error)
	GetFactByID(ctx context.Context, factID int64) (*FactForDedup, error)
}

// ReassignOptions tunes RunSingletonReassign.
type ReassignOptions struct {
	DryRun bool
	// Deadline, when set, stops the sweep from starting another person after
	// it; the rest stays pending for the next run.
	Deadline time.Time
	// StartAfter skips persons up to and including this id in the listing
	// order, to resume a deadline-cut run.
	StartAfter int64
	MaxPersons int
}

// ReassignStats are the counters of a singleton re-assignment sweep.
type ReassignStats struct {
	Persons        int64
	Singletons     int64 // singletons judged
	Joined         int64 // moved into another stance (the singleton is gone)
	Kept           int64 // the LLM still wants an own topic
	Failures       int64 // assignment or merge errors; the singleton stays
	Contradictions int64 // found while re-analyzing a moved fact in its new stance
}

// Add folds one person's counters into the sweep totals.
func (s *ReassignStats) Add(o ReassignStats) {
	s.Persons += o.Persons
	s.Singletons += o.Singletons
	s.Joined += o.Joined
	s.Kept += o.Kept
	s.Failures += o.Failures
	s.Contradictions += o.Contradictions
}

// ReassignSingletonsForPerson re-runs topic assignment for every one-fact
// stance of the person against the person's OTHER stances. A singleton was
// created when its fact arrived and nothing fit at the time — or when the
// assignment missed the fitting stance, the case the 2026-09-17 shortlist and
// decision fixes addressed. When the assignment now picks an existing stance
// the singleton is merged into it (the fact moves, contradiction bundles are
// repointed, the fact is re-analyzed against the new stance's facts and the
// stance's label and summary are refreshed); when it still says "new topic"
// the singleton stays.
func ReassignSingletonsForPerson(ctx context.Context, storer StanceReassignStorer, sm *StanceManager, p PersonRecord, dryRun bool) (ReassignStats, error) {
	var st ReassignStats
	stances, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
	if err != nil {
		return st, fmt.Errorf("loading stances: %w", err)
	}
	currentByID := make(map[int64]StanceDocument, len(stances))
	for _, s := range stances {
		currentByID[s.ID] = s
	}
	st.Persons = 1

	for _, s := range stances {
		if s.FactCount != 1 {
			continue
		}
		if _, live := currentByID[s.ID]; !live {
			continue // merged away as the destination of an earlier singleton
		}
		if len(currentByID) < 2 {
			break
		}
		members, err := storer.GetFactsForStance(ctx, s.ID)
		if err != nil {
			return st, fmt.Errorf("loading facts of stance %d: %w", s.ID, err)
		}
		if len(members) != 1 {
			continue // grew or emptied since the listing
		}
		fact, err := storer.GetFactByID(ctx, members[0].ID)
		if err != nil {
			return st, fmt.Errorf("loading fact %d: %w", members[0].ID, err)
		}
		st.Singletons++

		others := make([]StanceDocument, 0, len(currentByID)-1)
		for _, o := range currentByID {
			if o.ID != s.ID {
				others = append(others, o)
			}
		}
		decision, err := sm.assignTopic(ctx, p.ID, others, fact.NormalizedFactText, fact.Embedding, s.ID)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int64("stance_id", s.ID).Msg("Singleton re-assignment: topic assignment failed; stance stays")
			st.Failures++
			continue
		}
		target, ok := currentByID[decision.AssignedID]
		if decision.IsNew || !ok {
			st.Kept++
			continue
		}
		log.Info().Str("person", p.Name).
			Int64("stance_id", s.ID).Str("label", s.ClusterLabel).
			Int64("target_id", target.ID).Str("target_label", target.ClusterLabel).
			Str("reasoning", decision.Reasoning).Bool("dry_run", dryRun).
			Msg("Singleton re-assignment: fact joins an existing stance")
		if dryRun {
			st.Joined++
			continue
		}
		// The target keeps its label and summary until RefreshStanceMetadata
		// rewrites them from the merged fact set inside applyMergeSubgroup.
		sub := MergeSubgroup{StanceIDs: []int64{s.ID, target.ID}, Label: target.ClusterLabel, Summary: target.Summary}
		merged, contradictions, _, err := applyMergeSubgroup(ctx, storer, sm, p.ID, currentByID, sub)
		if err != nil || merged == 0 {
			log.Warn().Err(err).Str("person", p.Name).Int64("stance_id", s.ID).Int64("target_id", target.ID).
				Msg("Singleton re-assignment: merge failed; stance stays")
			st.Failures++
			continue
		}
		st.Joined++
		st.Contradictions += int64(contradictions)
		// Whichever of the two survived now holds both facts.
		for _, id := range sub.StanceIDs {
			if d, live := currentByID[id]; live {
				d.FactCount++
				currentByID[id] = d
			}
		}
	}

	log.Info().Str("person", p.Name).
		Int64("singletons", st.Singletons).Int64("joined", st.Joined).Int64("kept", st.Kept).
		Int64("failures", st.Failures).Int64("contradictions", st.Contradictions).
		Msg("Singleton re-assignment done for person")
	return st, nil
}

// RunSingletonReassign runs ReassignSingletonsForPerson over every person
// that has a singleton and another stance, within opts. A per-person failure
// is logged and skipped.
func RunSingletonReassign(ctx context.Context, storer StanceReassignStorer, sm *StanceManager, opts ReassignOptions) (ReassignStats, error) {
	var stats ReassignStats
	start := time.Now()

	persons, err := storer.ListPersonsWithSingletonStances(ctx)
	if err != nil {
		return stats, fmt.Errorf("listing persons with singleton stances: %w", err)
	}
	if opts.StartAfter != 0 {
		for i, p := range persons {
			if p.ID == opts.StartAfter {
				persons = persons[i+1:]
				break
			}
		}
	}
	pending := len(persons)
	if opts.MaxPersons > 0 && len(persons) > opts.MaxPersons {
		persons = persons[:opts.MaxPersons]
	}
	log.Info().Int("persons", len(persons)).Int("pending", pending).Bool("dry_run", opts.DryRun).Msg("Singleton re-assignment sweep started")

	var lastDone int64
	for i, p := range persons {
		if !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			log.Info().Int("done", i).Int("left", len(persons)-i).Int64("last_person_id", lastDone).
				Msg("Singleton re-assignment: deadline reached; the rest stays pending (resume with --start-after)")
			break
		}
		ps, perr := ReassignSingletonsForPerson(ctx, storer, sm, p, opts.DryRun)
		if perr != nil {
			log.Warn().Err(perr).Str("person", p.Name).Msg("Singleton re-assignment: per-person failure (continuing)")
		} else {
			stats.Add(ps)
		}
		lastDone = p.ID
		log.Info().Int("done", i+1).Int("of", len(persons)).Int64("person_id", p.ID).Msg("Singleton re-assignment: progress")
	}

	log.Info().Dur("elapsed", time.Since(start)).
		Int64("persons", stats.Persons).Int64("singletons", stats.Singletons).
		Int64("joined", stats.Joined).Int64("kept", stats.Kept).Int64("failures", stats.Failures).
		Int64("contradictions", stats.Contradictions).
		Msg("Singleton re-assignment sweep finished")
	return stats, nil
}
