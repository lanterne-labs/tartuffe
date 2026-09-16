package core

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakePersonDedupStorer serves canned candidates/members and records merges.
type fakePersonDedupStorer struct {
	candidates []PersonDuplicateCandidate
	members    map[int64]PersonDedupMember
	merged     [][2]int64
	renamed    map[int64]string
}

func (f *fakePersonDedupStorer) FindDuplicateCandidates(ctx context.Context, entityType string, threshold float64) ([]PersonDuplicateCandidate, error) {
	return f.candidates, nil
}

func (f *fakePersonDedupStorer) GetPersonsForDedup(ctx context.Context, ids []int64) ([]PersonDedupMember, error) {
	out := make([]PersonDedupMember, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.members[id])
	}
	return out, nil
}

func (f *fakePersonDedupStorer) GetAllDedupMembers(ctx context.Context) ([]PersonDedupMember, error) {
	out := make([]PersonDedupMember, 0, len(f.members))
	for _, m := range f.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Most facts wins, lowest id breaks ties; two curated members refuse.
func (f *fakePersonDedupStorer) ChooseCanonical(members []PersonDedupMember) (PersonDedupMember, bool) {
	curated := 0
	best := members[0]
	for _, m := range members {
		if m.Curated {
			curated++
		}
		if m.FactCount > best.FactCount || (m.FactCount == best.FactCount && m.ID < best.ID) {
			best = m
		}
	}
	if curated >= 2 {
		return PersonDedupMember{}, false
	}
	return best, true
}

func (f *fakePersonDedupStorer) MergePersons(ctx context.Context, fromID, intoID int64) error {
	f.merged = append(f.merged, [2]int64{fromID, intoID})
	delete(f.members, fromID)
	return nil
}

func (f *fakePersonDedupStorer) RenamePerson(ctx context.Context, id int64, name string) error {
	if f.renamed == nil {
		f.renamed = map[int64]string{}
	}
	f.renamed[id] = name
	m := f.members[id]
	m.Name = name
	f.members[id] = m
	return nil
}

// fakeAdjudicator confirms exactly the pairs listed in same.
type fakeAdjudicator struct {
	same map[[2]int64]bool
	err  map[[2]int64]error
}

func (a fakeAdjudicator) AdjudicatePair(ctx context.Context, x, y EntityRef) (*PairVerdict, error) {
	k := [2]int64{x.ID, y.ID}
	if err := a.err[k]; err != nil {
		return nil, err
	}
	return &PairVerdict{Same: a.same[k]}, nil
}

// Only LLM-confirmed pairs cluster; a rejected pair sharing a member with a
// confirmed one must not chain in; the survivor is the most-facts member and
// the dry run reports the merge without applying it.
func TestDedupePersons_ConfirmedPairsOnly(t *testing.T) {
	st := &fakePersonDedupStorer{
		candidates: []PersonDuplicateCandidate{
			{IDA: 1, IDB: 2, NameA: "eric ciotti", NameB: "éric ciotti"},
			{IDA: 2, IDB: 3, NameA: "éric ciotti", NameB: "eric zemmour"},
			{IDA: 4, IDB: 5, NameA: "x", NameB: "y"},
		},
		members: map[int64]PersonDedupMember{
			1: {ID: 1, Name: "eric ciotti", FactCount: 3},
			2: {ID: 2, Name: "éric ciotti", FactCount: 10},
			3: {ID: 3, Name: "eric zemmour", FactCount: 1},
			4: {ID: 4, Name: "x"},
			5: {ID: 5, Name: "y"},
		},
	}
	adj := fakeAdjudicator{
		same: map[[2]int64]bool{{1, 2}: true},
		err:  map[[2]int64]error{{4, 5}: errors.New("llm down")},
	}

	res, err := DedupePersons(context.Background(), st, adj, PersonDedupeOptions{Threshold: 0.45})
	if err != nil {
		t.Fatalf("DedupePersons: %v", err)
	}
	if res.PairsConfirmed != 1 || res.PairsRejected != 1 || res.PairErrors != 1 {
		t.Fatalf("pair counts = %+v", res)
	}
	if len(res.Clusters) != 1 {
		t.Fatalf("want one cluster, got %+v", res.Clusters)
	}
	c := res.Clusters[0]
	if c.SkipReason != "" || c.Canonical.ID != 2 {
		t.Fatalf("cluster = %+v, want canonical 2 (most facts)", c)
	}
	if len(c.Merges) != 1 || c.Merges[0].From.ID != 1 || c.Merges[0].Applied {
		t.Fatalf("dry run must report 1 -> 2 without applying, got %+v", c.Merges)
	}
	if res.WouldMerge != 1 || res.Merged != 0 || len(st.merged) != 0 {
		t.Fatalf("nothing may be merged in dry run, got %+v merged=%v", res, st.merged)
	}
}

// Apply merges every non-canonical member into the survivor; a cluster with two
// curated members is skipped and reported as such.
func TestDedupePersons_ApplyAndCuratedSkip(t *testing.T) {
	st := &fakePersonDedupStorer{
		candidates: []PersonDuplicateCandidate{
			{IDA: 1, IDB: 2},
			{IDA: 2, IDB: 3},
			{IDA: 7, IDB: 8},
		},
		members: map[int64]PersonDedupMember{
			1: {ID: 1, FactCount: 1}, 2: {ID: 2, FactCount: 5}, 3: {ID: 3, FactCount: 2},
			7: {ID: 7, Curated: true}, 8: {ID: 8, Curated: true},
		},
	}
	adj := fakeAdjudicator{same: map[[2]int64]bool{{1, 2}: true, {2, 3}: true, {7, 8}: true}}

	res, err := DedupePersons(context.Background(), st, adj, PersonDedupeOptions{Apply: true})
	if err != nil {
		t.Fatalf("DedupePersons: %v", err)
	}
	if res.Merged != 2 || res.MergeFailed != 0 || res.ClustersSkipped != 1 {
		t.Fatalf("result = %+v", res)
	}
	sort.Slice(st.merged, func(i, j int) bool { return st.merged[i][0] < st.merged[j][0] })
	if len(st.merged) != 2 || st.merged[0] != [2]int64{1, 2} || st.merged[1] != [2]int64{3, 2} {
		t.Fatalf("merges = %v, want 1->2 and 3->2", st.merged)
	}
	var skipped *PersonDedupeCluster
	for i := range res.Clusters {
		if res.Clusters[i].SkipReason != "" {
			skipped = &res.Clusters[i]
		}
	}
	if skipped == nil || skipped.SkipReason != PersonClusterSkipCurated {
		t.Fatalf("expected the curated cluster to be skipped, got %+v", res.Clusters)
	}
}

// A confirmed cluster above the safety cap is skipped without loading members.
func TestDedupePersons_CapSkip(t *testing.T) {
	st := &fakePersonDedupStorer{members: map[int64]PersonDedupMember{}}
	adj := fakeAdjudicator{same: map[[2]int64]bool{}}
	for i := int64(1); i <= int64(maxConfirmedClusterSize); i++ {
		st.candidates = append(st.candidates, PersonDuplicateCandidate{IDA: i, IDB: i + 1})
		adj.same[[2]int64{i, i + 1}] = true
	}
	res, err := DedupePersons(context.Background(), st, adj, PersonDedupeOptions{Apply: true})
	if err != nil {
		t.Fatalf("DedupePersons: %v", err)
	}
	if res.ClustersSkipped != 1 || len(res.Clusters) != 1 || res.Clusters[0].SkipReason != PersonClusterSkipCap {
		t.Fatalf("expected one cap-skipped cluster, got %+v", res)
	}
	if len(st.merged) != 0 {
		t.Fatalf("cap-skipped cluster must not merge, got %v", st.merged)
	}
}

// Variant names collide on the canonical form; apply merges into the survivor
// and canonicalizes the remaining names.
func TestUnaccentPersons(t *testing.T) {
	st := &fakePersonDedupStorer{
		members: map[int64]PersonDedupMember{
			1: {ID: 1, Name: "éric ciotti", FactCount: 2},
			2: {ID: 2, Name: "eric ciotti", FactCount: 9},
			3: {ID: 3, Name: "d’azur"},
			4: {ID: 4, Name: "marine le pen"},
		},
	}
	dry, err := UnaccentPersons(context.Background(), st, false)
	if err != nil {
		t.Fatalf("UnaccentPersons dry: %v", err)
	}
	if dry.DupGroups != 1 || len(dry.Groups) != 1 || dry.Groups[0].Survivor.ID != 2 || len(st.merged) != 0 {
		t.Fatalf("dry run = %+v merged=%v", dry, st.merged)
	}

	res, err := UnaccentPersons(context.Background(), st, true)
	if err != nil {
		t.Fatalf("UnaccentPersons apply: %v", err)
	}
	if res.MergedAway != 1 || len(st.merged) != 1 || st.merged[0] != [2]int64{1, 2} {
		t.Fatalf("expected 1 merged into 2, got %+v merged=%v", res, st.merged)
	}
	if res.Renamed != 1 || st.renamed[3] != "d'azur" {
		t.Fatalf("expected only the curly apostrophe name to be canonicalized, got renamed=%v (%d)", st.renamed, res.Renamed)
	}
}
