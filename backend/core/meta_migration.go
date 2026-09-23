package core

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// SyntheticMeta is an LLM-synthesized meta-fact from before claim groups.
// ParentID is the group it is itself a member of (0 when active).
type SyntheticMeta struct {
	ID       int64
	Text     string
	ParentID int64
}

// MetaMigrationStorer is the storage view the meta migration needs.
// *storage.PostgresStorer implements it.
type MetaMigrationStorer interface {
	ListSyntheticMetas(ctx context.Context) ([]SyntheticMeta, error)
	GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error)
	MostConcreteFact(ctx context.Context, factIDs []int64) (int64, error)
	MergeFactIntoGroup(ctx context.Context, from, into int64) error
	DeleteSyntheticMeta(ctx context.Context, metaID int64) error
}

// MetaMigrationStats are the counters of one migration run. A dry run fills
// only the plan (Metas, Nested, Active).
type MetaMigrationStats struct {
	Metas     int // synthetic metas found
	Nested    int // of which members of another group
	Active    int // of which heading a group
	Flattened int // nested metas merged into their group
	Promoted  int // metas replaced by their most concrete real member as parent
	Deleted   int // synthetic rows removed
	Failed    int // metas left untouched by an error (logged once); a re-run retries them
}

// MigrateSyntheticMetas turns every synthetic meta-fact into a claim group
// whose parent is an actual statement, then removes the synthetic rows:
//
//  1. a meta that is itself a member of another group is merged into that
//     group — members, contradictions, stance and key-fact references follow;
//     one nested under a nested meta waits until that one is done;
//  2. every remaining meta's most concrete real member becomes the parent
//     (storage.MergeFactIntoGroup, which gives it the meta's visible stance
//     slot);
//  3. the synthetic rows, now empty members, are deleted.
//
// Every step is atomic per meta, and a meta that errors is left as it was
// and not retried within the run, so a run can simply be repeated. Without
// apply it only reports the plan.
func MigrateSyntheticMetas(ctx context.Context, storer MetaMigrationStorer, apply bool) (MetaMigrationStats, error) {
	var st MetaMigrationStats
	metas, err := storer.ListSyntheticMetas(ctx)
	if err != nil {
		return st, err
	}
	st.Metas = len(metas)
	synthetic := make(map[int64]bool, len(metas))
	nestedIn := make(map[int64]int64, len(metas))
	for _, m := range metas {
		synthetic[m.ID] = true
		if m.ParentID != 0 {
			st.Nested++
			nestedIn[m.ID] = m.ParentID
		} else {
			st.Active++
		}
	}
	if !apply {
		return st, nil
	}
	failed := map[int64]bool{}
	fail := func(id int64, step string, err error) {
		failed[id] = true
		st.Failed++
		log.Error().Err(err).Int64("meta_id", id).Str("step", step).Msg("Meta migration: meta left untouched")
	}

	// 1. Flatten: a nested meta merges into its group once that group's
	// parent is active (not itself a nested meta still waiting).
	done := map[int64]bool{}
	for {
		progressed := false
		for id, parent := range nestedIn {
			if done[id] || failed[id] {
				continue
			}
			if _, parentNested := nestedIn[parent]; parentNested && !done[parent] {
				continue
			}
			into := parent
			if done[parent] {
				into = nestedIn[parent] // the parent was merged upward; follow it
				for done[into] {
					into = nestedIn[into]
				}
			}
			if err := storer.MergeFactIntoGroup(ctx, id, into); err != nil {
				fail(id, "flatten", err)
				continue
			}
			done[id] = true
			st.Flattened++
			progressed = true
		}
		if !progressed {
			break
		}
	}

	// 2. Promote the most concrete real member of every meta heading a group.
	for _, m := range metas {
		if m.ParentID != 0 || failed[m.ID] {
			continue
		}
		members, err := storer.GetGroupMembers(ctx, m.ID)
		if err != nil {
			return st, err
		}
		var real []int64
		for _, f := range members {
			if !synthetic[f.ID] {
				real = append(real, f.ID)
			}
		}
		if len(real) == 0 {
			fail(m.ID, "promote", fmt.Errorf("no real member to promote"))
			continue
		}
		parent, err := storer.MostConcreteFact(ctx, real)
		if err != nil {
			fail(m.ID, "promote", err)
			continue
		}
		if err := storer.MergeFactIntoGroup(ctx, m.ID, parent); err != nil {
			fail(m.ID, "promote", err)
			continue
		}
		st.Promoted++
	}

	// 3. Delete the synthetic rows (the storage refuses any that still has
	// members or contradictions).
	for _, m := range metas {
		if failed[m.ID] {
			continue
		}
		if err := storer.DeleteSyntheticMeta(ctx, m.ID); err != nil {
			fail(m.ID, "delete", err)
			continue
		}
		st.Deleted++
	}
	log.Info().Int("metas", st.Metas).Int("flattened", st.Flattened).Int("promoted", st.Promoted).
		Int("deleted", st.Deleted).Int("failed", st.Failed).Msg("Meta migration finished")
	return st, nil
}
