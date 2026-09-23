package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// migrationStorer is an in-memory group store: parent → members, texts, the
// synthetic flags, and the same guards the real storage enforces.
type migrationStorer struct {
	text      map[int64]string
	synthetic map[int64]bool
	parentOf  map[int64]int64 // member → parent
	deleted   map[int64]bool
	merges    [][2]int64
}

func (m *migrationStorer) members(parent int64) []int64 {
	var out []int64
	for f, p := range m.parentOf {
		if p == parent && !m.deleted[f] {
			out = append(out, f)
		}
	}
	return out
}

func (m *migrationStorer) ListSyntheticMetas(ctx context.Context) ([]SyntheticMeta, error) {
	var out []SyntheticMeta
	for id := int64(1); id <= 200; id++ {
		if m.synthetic[id] && !m.deleted[id] {
			out = append(out, SyntheticMeta{ID: id, Text: m.text[id], ParentID: m.parentOf[id]})
		}
	}
	return out, nil
}
func (m *migrationStorer) GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error) {
	var out []FactForFold
	for _, f := range m.members(parentID) {
		out = append(out, FactForFold{ID: f, Text: m.text[f]})
	}
	return out, nil
}
func (m *migrationStorer) MostConcreteFact(ctx context.Context, ids []int64) (int64, error) {
	best := ids[0]
	for _, id := range ids[1:] {
		if strings.Count(m.text[id], "1") > strings.Count(m.text[best], "1") {
			best = id
		}
	}
	return best, nil
}
func (m *migrationStorer) MergeFactIntoGroup(ctx context.Context, from, into int64) error {
	if m.parentOf[into] != 0 && m.parentOf[into] != from {
		return fmt.Errorf("into %d is a member of %d", into, m.parentOf[into])
	}
	delete(m.parentOf, into)
	for f, p := range m.parentOf {
		if p == from {
			m.parentOf[f] = into
		}
	}
	m.parentOf[from] = into
	m.merges = append(m.merges, [2]int64{from, into})
	return nil
}
func (m *migrationStorer) DeleteSyntheticMeta(ctx context.Context, id int64) error {
	if !m.synthetic[id] {
		return fmt.Errorf("%d is not synthetic", id)
	}
	if len(m.members(id)) > 0 {
		return fmt.Errorf("%d still has members", id)
	}
	m.deleted[id] = true
	return nil
}

// Meta 100 has real members 1 and 2 (2 more concrete) and a nested synthetic
// meta 101, which itself has real members 3 and 4. After migration: one group
// headed by the real fact 2, holding 1, 3, 4 (and the synthetic rows gone).
func TestMigrateSyntheticMetas(t *testing.T) {
	st := &migrationStorer{
		text:      map[int64]string{1: "vague", 2: "precise 11", 3: "other wording", 4: "wording 1", 100: "synth", 101: "synth nested"},
		synthetic: map[int64]bool{100: true, 101: true},
		parentOf:  map[int64]int64{1: 100, 2: 100, 101: 100, 3: 101, 4: 101},
		deleted:   map[int64]bool{},
	}

	dry, err := MigrateSyntheticMetas(context.Background(), st, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry != (MetaMigrationStats{Metas: 2, Nested: 1, Active: 1}) || len(st.merges) != 0 || len(st.deleted) != 0 {
		t.Fatalf("dry run = %+v merges=%v deleted=%v, want the plan and no writes", dry, st.merges, st.deleted)
	}

	res, err := MigrateSyntheticMetas(context.Background(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if res != (MetaMigrationStats{Metas: 2, Nested: 1, Active: 1, Flattened: 1, Promoted: 1, Deleted: 2}) {
		t.Fatalf("apply = %+v", res)
	}
	got := map[int64]bool{}
	for _, f := range st.members(2) {
		got[f] = true
	}
	if len(got) != 3 || !got[1] || !got[3] || !got[4] {
		t.Fatalf("group 2 holds %v, want the real facts 1, 3, 4", st.members(2))
	}
	if st.parentOf[2] != 0 {
		t.Fatal("the promoted parent must be active")
	}
	if !st.deleted[100] || !st.deleted[101] {
		t.Fatal("both synthetic rows must be deleted")
	}

	// Re-running finds nothing left to do.
	again, err := MigrateSyntheticMetas(context.Background(), st, true)
	if err != nil || again.Metas != 0 {
		t.Fatalf("re-run = %+v, %v; want nothing to migrate", again, err)
	}
}

// failingMerge refuses one merge; everything else behaves.
type failingMerge struct {
	*migrationStorer
	refuse int64
}

func (f *failingMerge) MergeFactIntoGroup(ctx context.Context, from, into int64) error {
	if from == f.refuse {
		return fmt.Errorf("refused %d", from)
	}
	return f.migrationStorer.MergeFactIntoGroup(ctx, from, into)
}

// A chain (102 nested in 101 nested in 100) flattens bottom-up through its
// parents; a meta whose merge fails is counted once, left untouched, and not
// deleted, while the rest of the run goes through.
func TestMigrateSyntheticMetasChainAndFailure(t *testing.T) {
	base := &migrationStorer{
		text:      map[int64]string{1: "a 1", 2: "b", 3: "c 11", 5: "e", 100: "s", 101: "s", 102: "s", 200: "s2"},
		synthetic: map[int64]bool{100: true, 101: true, 102: true, 200: true},
		parentOf:  map[int64]int64{1: 100, 101: 100, 2: 101, 102: 101, 3: 102, 5: 200},
		deleted:   map[int64]bool{},
	}
	st := &failingMerge{migrationStorer: base, refuse: 200}
	res, err := MigrateSyntheticMetas(context.Background(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flattened != 2 || res.Promoted != 1 || res.Failed != 1 || res.Deleted != 3 {
		t.Fatalf("run = %+v, want 2 flattened, 1 promoted, 1 failed (200, counted once), 3 deleted", res)
	}
	got := map[int64]bool{}
	for _, f := range base.members(3) {
		got[f] = true
	}
	if len(got) != 2 || !got[1] || !got[2] {
		t.Fatalf("group 3 (most concrete) holds %v, want the real facts 1 and 2", base.members(3))
	}
	if base.deleted[200] {
		t.Fatal("the failed meta must be left untouched")
	}
}
