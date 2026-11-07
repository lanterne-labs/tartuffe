package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/core"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// The three uses of MergeFactIntoGroup: a fresh restatement joins a group, a
// member is promoted to parent, and two existing groups merge. Every time the
// members follow, contradictions move to the parent (redundant and self-pairs
// dropped), stance key facts and group bases follow, and the parent inherits
// a stance slot only where it has none.
func TestMergeFactIntoGroup(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	pid, _, err := storer.AddPerson(ctx, "group merge person")
	require.NoError(t, err)
	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		return id
	}
	stance := func(label string, factIDs ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: pid, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label,
			KeyFactIDs: factIDs, FactCount: len(factIDs),
		})
		require.NoError(t, err)
		for _, f := range factIDs {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}
	contradiction := func(a, b int64) {
		if a > b {
			a, b = b, a
		}
		_, err := storer.pool.Exec(ctx,
			"INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, 't')", pid, a, b)
		require.NoError(t, err)
	}
	members := func(parent int64) []int64 {
		rows, err := storer.pool.Query(ctx,
			"SELECT specific_fact_id FROM fact_relations WHERE general_fact_id = $1 AND relation_type = 'duplicate' ORDER BY specific_fact_id", parent)
		require.NoError(t, err)
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		return out
	}
	pairsOf := func(f int64) [][2]int64 {
		rows, err := storer.pool.Query(ctx,
			"SELECT fact_a_id, fact_b_id FROM contradictions WHERE fact_a_id = $1 OR fact_b_id = $1 ORDER BY 1, 2", f)
		require.NoError(t, err)
		defer rows.Close()
		var out [][2]int64
		for rows.Next() {
			var p [2]int64
			require.NoError(t, rows.Scan(&p[0], &p[1]))
			out = append(out, p)
		}
		return out
	}

	x := fact("Il veut réduire le déficit.")
	other := fact("Il refuse toute économie.")
	fiscal := stance("finances publiques", x, other)
	contradiction(x, other)

	// 1. A fresh restatement joins x's group.
	fresh := fact("Il veut réduire le déficit, en 2027.")
	_, err = storer.pool.Exec(ctx, "UPDATE facts SET updated_at = NOW() - interval '1 day' WHERE id = $1", x)
	require.NoError(t, err)
	require.NoError(t, storer.MergeFactIntoGroup(ctx, fresh, x))
	require.Equal(t, []int64{fresh}, members(x))
	var marked bool
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT updated_at > NOW() - interval '1 minute' FROM facts WHERE id = $1", x).Scan(&marked))
	require.True(t, marked, "the parent is marked so the next pass re-detects its person")
	var inStance bool
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM stance_document_facts WHERE fact_id = $1)", fresh).Scan(&inStance))
	require.False(t, inStance, "a fresh member does not take a stance slot of its own here; the parent holds it")

	// 2. The member is more concrete: promote it to parent.
	require.NoError(t, storer.MergeFactIntoGroup(ctx, x, fresh))
	require.Equal(t, []int64{x}, members(fresh), "the old parent is now a member")
	require.Empty(t, members(x))
	require.Equal(t, [][2]int64{{min(fresh, other), max(fresh, other)}}, pairsOf(fresh), "the contradiction follows the parent")
	require.Empty(t, pairsOf(x))
	var keys []int64
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT key_fact_ids FROM stance_documents WHERE id = $1", fiscal).Scan(&keys))
	require.ElementsMatch(t, []int64{fresh, other}, keys, "key facts name the active parent")
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM stance_document_facts WHERE fact_id = $1 AND stance_document_id = $2)", fresh, fiscal).Scan(&inStance))
	require.True(t, inStance, "the new parent takes the stance slot it had none of")

	// 3. Two groups merge: y (with a member) goes into fresh's group; a pair
	// that becomes redundant, and one that becomes a self-pair, are dropped.
	y := fact("Il veut ramener le déficit sous 3 %.")
	yMember := fact("Déficit sous 3 % en 2029.")
	require.NoError(t, storer.MergeFactIntoGroup(ctx, yMember, y))
	contradiction(y, other) // redundant with (fresh, other)
	contradiction(y, fresh) // becomes a self-pair
	_, err = storer.pool.Exec(ctx, "UPDATE contradictions SET base_fact_id = $1 WHERE fact_a_id = $2 OR fact_b_id = $2", y, y)
	require.NoError(t, err)
	require.NoError(t, storer.MergeFactIntoGroup(ctx, y, fresh))
	require.ElementsMatch(t, []int64{x, y, yMember}, members(fresh), "y's members follow it")
	require.Equal(t, [][2]int64{{min(fresh, other), max(fresh, other)}}, pairsOf(fresh), "redundant and self-pairs dropped")
	var danglingBase int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM contradictions WHERE base_fact_id = $1", y).Scan(&danglingBase))
	require.Zero(t, danglingBase, "group bases follow the parent")

	// Guards.
	require.Error(t, storer.MergeFactIntoGroup(ctx, fresh, fresh))
	require.Error(t, storer.MergeFactIntoGroup(ctx, other, x), "x is a member now; merging into it would nest groups")

	// MostConcreteFact: digits first, then length.
	most, err := storer.MostConcreteFact(ctx, []int64{x, fresh, y})
	require.NoError(t, err)
	require.Equal(t, fresh, most, "'en 2027' has four digits, 'sous 3 %' one")
}

// A fact has one parent: a member of one group cannot join another group,
// for example through a second pair of one detection run. A merge into its
// own parent stays allowed.
func TestMergeFactIntoGroupRefusesSecondParent(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		return id
	}
	parentsOf := func(f int64) []int64 {
		rows, err := storer.pool.Query(ctx,
			"SELECT general_fact_id FROM fact_relations WHERE specific_fact_id = $1 AND relation_type = 'duplicate' ORDER BY 1", f)
		require.NoError(t, err)
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		return out
	}

	a := fact("Il veut réduire le déficit.")
	b := fact("Il veut réduire le déficit en 2027.")
	c := fact("Il promet de réduire le déficit.")
	require.NoError(t, storer.MergeFactIntoGroup(ctx, a, b))
	require.Error(t, storer.MergeFactIntoGroup(ctx, a, c), "a has parent b; a second parent is refused")
	require.Equal(t, []int64{b}, parentsOf(a), "the refused merge changes nothing")
	require.NoError(t, storer.MergeFactIntoGroup(ctx, a, b), "a merge into its own parent is allowed")
	require.Equal(t, []int64{b}, parentsOf(a))
}

// Members come back most concrete first (digits, then length), deduplicated,
// within the character budget — the first one always; a fact without members
// is absent.
func TestGroupMemberTexts(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		return id
	}
	parent := fact("Un plan pour ramener le déficit.")
	vague := fact("Il veut réduire le déficit.")
	precise := fact("Un effort de 43,8 milliards d'euros pour ramener le déficit sous 5 % du PIB.")
	fiveYears := fact("Un plan de 5 ans pour ramener les déficits sous la croissance.")
	for _, m := range []int64{vague, precise, fiveYears} {
		require.NoError(t, storer.MergeFactIntoGroup(ctx, m, parent))
	}
	lone := fact("un fait sans groupe")

	got, err := storer.GroupMemberTexts(ctx, []int64{parent, lone})
	require.NoError(t, err)
	require.Equal(t, []string{
		"Un effort de 43,8 milliards d'euros pour ramener le déficit sous 5 % du PIB.",
		"Un plan de 5 ans pour ramener les déficits sous la croissance.",
		"Il veut réduire le déficit.",
	}, got[parent], "most digits first, then longest")
	_, hasLone := got[lone]
	require.False(t, hasLone, "a fact without members is absent")

	// Over budget: a group of long members keeps what fits, the first always.
	big := fact("Gros groupe.")
	for i := 0; i < 5; i++ {
		m := fact(fmt.Sprintf("Membre %d : %s", i, strings.Repeat("x", 300)))
		require.NoError(t, storer.MergeFactIntoGroup(ctx, m, big))
	}
	got, err = storer.GroupMemberTexts(ctx, []int64{big})
	require.NoError(t, err)
	require.Len(t, got[big], 2, "two ~311-char members fit in 800, a third does not")

	empty, err := storer.GroupMemberTexts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// The review's storage: groups listed by their latest join and by id, members
// with texts, and un-grouping a member makes it active again in the parent's
// stance (and refuses a non-member).
func TestClaimGroupReviewStorage(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	pid, _, err := storer.AddPerson(ctx, "group review person")
	require.NoError(t, err)
	fact := func(text, url string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		var sid int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, 'news') RETURNING id", id, url).Scan(&sid))
		_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, pid)
		require.NoError(t, err)
		return id
	}
	parent := fact("Effort de 43,8 milliards.", "https://g.test/p")
	a := fact("Réduire le déficit.", "https://g.test/a")
	b := fact("Deux jours fériés supprimés.", "https://g.test/b")
	st, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID: pid, ClusterLabel: "finances", ClusterEmbedding: mockEmbedding(), Summary: "s", KeyFactIDs: []int64{parent},
	})
	require.NoError(t, err)
	require.NoError(t, storer.AddFactToStance(ctx, st, parent))
	require.NoError(t, storer.MergeFactIntoGroup(ctx, a, parent))
	require.NoError(t, storer.MergeFactIntoGroup(ctx, b, parent))

	groups, err := storer.ListClaimGroups(ctx, time.Now().Add(-time.Minute), nil)
	require.NoError(t, err)
	require.Equal(t, []core.ClaimGroup{{ParentID: parent, PersonID: pid, PersonName: "group review person", Text: "Effort de 43,8 milliards."}}, groups)
	later, err := storer.ListClaimGroups(ctx, time.Now().Add(time.Minute), nil)
	require.NoError(t, err)
	require.Empty(t, later, "no join since then")
	only, err := storer.ListClaimGroups(ctx, time.Now().Add(-time.Minute), []int64{parent + 1000})
	require.NoError(t, err)
	require.Empty(t, only)

	members, err := storer.GetGroupMembers(ctx, parent)
	require.NoError(t, err)
	require.Equal(t, []core.FactForFold{{ID: a, Text: "Réduire le déficit."}, {ID: b, Text: "Deux jours fériés supprimés."}}, members)

	require.NoError(t, storer.UngroupFact(ctx, parent, b))
	f, err := storer.GetFactByID(ctx, b)
	require.NoError(t, err)
	require.True(t, f.IsCanonical, "an ungrouped member is active again")
	var inStance bool
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM stance_document_facts WHERE fact_id = $1 AND stance_document_id = $2)", b, st).Scan(&inStance))
	require.True(t, inStance, "the freed fact takes the parent's stance")
	require.Error(t, storer.UngroupFact(ctx, parent, b), "ungrouping a non-member is an error")
}

// The migration's shape (39 of 1,939 slots on prod): the group's visible slot
// is S1, held by the old parent; the member being promoted carries its own,
// invisible link in S2. After the merge the new parent is in S1 (where the
// claim was shown), not in S2; S1's key facts name it, S2's no longer do; and
// its cached pair verdicts are re-opened, since the judges now see more
// wordings of it.
func TestMergeFactIntoGroupPromotionTakesVisibleSlot(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents, fact_pair_checks RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	pid, _, err := storer.AddPerson(ctx, "promotion slot person")
	require.NoError(t, err)
	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		return id
	}
	stance := func(label string, keys []int64, members ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: pid, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label, KeyFactIDs: keys,
		})
		require.NoError(t, err)
		for _, f := range members {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}
	old := fact("Un plan pour le déficit.")
	member := fact("Un effort de 43,8 milliards.")
	unrelated := fact("Autre sujet.")
	s1 := stance("budget", []int64{old}, old)
	s2 := stance("fiscalité", []int64{member, unrelated}, member, unrelated)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", old, member)
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction) VALUES ($1, $2, $3, false)", pid, min(member, unrelated), max(member, unrelated))
	require.NoError(t, err)

	require.NoError(t, storer.MergeFactIntoGroup(ctx, old, member))

	inStance := func(f, s int64) bool {
		var ok bool
		require.NoError(t, storer.pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM stance_document_facts WHERE fact_id = $1 AND stance_document_id = $2)", f, s).Scan(&ok))
		return ok
	}
	require.True(t, inStance(member, s1), "the promoted parent takes the visible slot")
	require.False(t, inStance(member, s2), "its hidden link gives way")
	keys := func(s int64) []int64 {
		var k []int64
		require.NoError(t, storer.pool.QueryRow(ctx, "SELECT key_fact_ids FROM stance_documents WHERE id = $1", s).Scan(&k))
		return k
	}
	require.Equal(t, []int64{member}, keys(s1))
	require.Equal(t, []int64{unrelated}, keys(s2), "a stance never names a key fact it does not hold")
	var checks int
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT count(*) FROM fact_pair_checks WHERE $1 IN (fact_a_id, fact_b_id)", member).Scan(&checks))
	require.Zero(t, checks, "the new parent's cached verdicts are re-opened")
}

// TestClaimGroupParent: a member resolves to its group's parent, an
// ungrouped fact (or a parent) to itself.
func TestClaimGroupParent(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		return id
	}
	parent, member, alone := fact("appeal ruling, 15 months"), fact("appeal ruling, 45 months of which 30 suspended"), fact("other")
	require.NoError(t, storer.MergeFactIntoGroup(ctx, member, parent))

	for _, tc := range []struct{ fact, want int64 }{{member, parent}, {parent, parent}, {alone, alone}} {
		got, err := storer.ClaimGroupParent(ctx, tc.fact)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "fact %d", tc.fact)
	}
}

// TestStanceContradictionGroupsCarryRestatements: a contradiction card shows
// the other wordings of its base fact and of each contradicting fact.
func TestStanceContradictionGroupsCarryRestatements(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	pid, _, err := storer.AddPerson(ctx, "restatements person")
	require.NoError(t, err)
	fact := func(text, url string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		_, err := storer.pool.Exec(ctx, "INSERT INTO fact_sources (fact_id, source_url) VALUES ($1, $2)", id, url)
		require.NoError(t, err)
		return id
	}
	base := fact("affirms innocence", "https://tf1info.fr/a")
	ruling := fact("convicted on appeal, 15 months", "https://lemonde.fr/b")
	sameRuling := fact("convicted on appeal, 45 months of which 30 suspended", "https://liberation.fr/c")
	require.NoError(t, storer.MergeFactIntoGroup(ctx, sameRuling, ruling))

	stance, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID: pid, ClusterLabel: "justice", ClusterEmbedding: mockEmbedding(), Summary: "s",
		KeyFactIDs: []int64{base, ruling}, FactCount: 2,
	})
	require.NoError(t, err)
	require.NoError(t, storer.AddFactToStance(ctx, stance, base))
	require.NoError(t, storer.AddFactToStance(ctx, stance, ruling))
	a, b := base, ruling
	if a > b {
		a, b = b, a
	}
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, base_fact_id, reason) VALUES ($1, $2, $3, $4, 'r')", pid, a, b, base)
	require.NoError(t, err)

	groups, err := storer.GetStanceContradictionGroups(ctx, stance, 0)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Empty(t, groups[0].BaseRestatements, "the base fact has no other wording")
	require.Len(t, groups[0].ContradictingFacts, 1)
	also := groups[0].ContradictingFacts[0].Restatements
	require.Len(t, also, 1)
	require.Equal(t, "convicted on appeal, 45 months of which 30 suspended", also[0].FactText)
	require.Equal(t, "https://liberation.fr/c", also[0].SourceURL)
}
