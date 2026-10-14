package frpolitics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"digital-mirror/backend/core"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// countingStancer counts the LLM steps of the placement (subject label and
// assignment) with canned answers, and applies with the real stance manager.
// A label for a text outside subjects fails the test.
type countingStancer struct {
	*core.StanceManager
	t               *testing.T
	subjects        map[string]string // text read by SubjectLabel → subject label
	labels, assigns int
}

func (c *countingStancer) SubjectLabel(_ context.Context, text string) (string, error) {
	c.labels++
	subject, ok := c.subjects[text]
	if !ok {
		c.t.Errorf("unexpected subject label call for %q", text)
		return "", fmt.Errorf("no canned subject for %q", text)
	}
	return subject, nil
}

// AssignStance opens a new stance for each ballot it is asked about.
func (c *countingStancer) AssignStance(context.Context, int64, int64, string, []float32) (core.StanceDecision, error) {
	c.assigns++
	return core.StanceDecision{Verdict: core.VerdictNewStance, NewLabel: fmt.Sprintf("sujet %d", c.assigns)}, nil
}

// TestIsBudgetLaw: real scrutin titles of budget laws and of ordinary laws.
func TestIsBudgetLaw(t *testing.T) {
	for title, want := range map[string]bool{
		"l'ensemble du projet de loi de finances pour 2026":                                                                                                                                  true,
		"la motion de rejet préalable, déposée par Mme Mathilde Panot, du projet de loi de finances pour 2026":                                                                               true,
		"l'amendement n° 2210 de Mme Pochon à l'article 12 du projet de loi de finances pour 2026 (première lecture)":                                                                        true,
		"l'ensemble du projet de loi de finances rectificative pour 2025":                                                                                                                    true,
		"l'amendement n° 1494 de M. Coquerel à l'article 8 du projet de loi de financement de la sécurité sociale pour 2025":                                                                 true,
		"l'ensemble du Projet de loi de financement de la Sécurité sociale pour 2026":                                                                                                        true,
		"l'ensemble de la proposition de loi visant à la nationalisation d'ArcelorMittal France afin de préserver la souveraineté industrielle de la France (première lecture).":             false,
		"le sous-amendement n° 372 de M. Verny à l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France": false,
		"l'ensemble du projet de loi de programmation des finances publiques pour les années 2023 à 2027":                                                                                    false,
		"la motion de censure déposée en application de l'article 49, alinéa 2, de la Constitution":                                                                                          false,
	} {
		if got := isBudgetLaw(title); got != want {
			t.Errorf("isBudgetLaw(%.70q) = %v, want %v", title, got, want)
		}
	}
}

// TestStoreBallotsPlacesBySubject:
//   - an ordinary law gets one subject label for all its scrutins: the second
//     scrutin reuses the stored subject (no label call, no assignment), and
//     each ballot joins the stance of the voter's first ballot on the law;
//   - each scrutin of a budget law gets its own label; a resumed scrutin
//     reuses its stored label;
//   - apply makes no LLM call, and every ballot has a stance.
func TestStoreBallotsPlacesBySubject(t *testing.T) {
	storer, _ := setupStoreTest(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	noLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected LLM call to %s", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(noLLM.Close)
	t.Setenv("OPEN_AI_API", noLLM.URL+"/v1")
	t.Setenv("OPEN_AI_KEY", "test")
	t.Setenv("FALLBACK_URL", noLLM.URL+"/v1")
	t.Setenv("FALLBACK_KEY", "test")
	sm, err := core.NewStanceManagerFromEnv(storer)
	require.NoError(t, err)

	const (
		arcelorSummary = "Nationalise ArcelorMittal France"
		energyPhrase   = "l'amendement n° 2210 (EcoS) visant à créer une contribution exceptionnelle sur les bénéfices des énergéticiens, déposé sur le texte de loi : « Fixe le budget de l'État pour 2026 »"
		defensePhrase  = "l'amendement n° 845 (DR) visant à augmenter les crédits de la mission « Défense », déposé sur le texte de loi : « Fixe le budget de l'État pour 2026 »"
	)
	stancer := &countingStancer{StanceManager: sm, t: t, subjects: map[string]string{
		prepareLawPhrase(arcelorSummary): "politique industrielle",
		energyPhrase:                     "fiscalité de l'énergie",
		defensePhrase:                    "politique de défense",
	}}
	deputies := []VoteEvent{
		{DisplayName: "Placement Alpha", Position: VoteFor},
		{DisplayName: "Placement Beta", Position: VoteAgainst},
		{DisplayName: "Placement Gamma", Position: VoteAbstention},
	}
	organes := map[string]OrganeInfo{"PO1": {OrganeRef: "PO1", LongName: "Groupe Placement"}}
	emb := make([]float32, 4096)
	emb[0] = 1
	// scrutin stores the ballots of the deputies (and of the party when
	// withParty) on one scrutin of the law at dossier.
	scrutin := func(dossier, key, title, summary, phrase string, deputies []VoteEvent, withParty bool) {
		t.Helper()
		law := Law{Key: key, Title: title, URL: dossier}
		records := deriveDeputyFacts(law, deputies, LawSummary{}, phrase)
		if withParty {
			records = append(records, derivePartyMajorityFacts(law, LawGroupStats{"PO1": {Position: VoteFor, For: 9, Against: 1}}, 0.8, organes, LawSummary{}, phrase)...)
		}
		for i := range records {
			records[i].LawTitle = title
			records[i].LawSummary = summary
		}
		require.NoError(t, storeBallots(ctx, storer, stancer, records, map[string][]float32{phrase: emb}))
	}
	subjects := func(key string) map[string]bool {
		t.Helper()
		rows, err := storer.Pool().Query(ctx, `SELECT DISTINCT metadata->>'vote_subject' FROM fact_sources WHERE metadata->>'law_key' = $1`, key)
		require.NoError(t, err)
		out := map[string]bool{}
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			out[s] = true
		}
		require.NoError(t, rows.Err())
		return out
	}

	// Ordinary law: two scrutins, the bill worded differently by each summary.
	const arcelor = "https://an.test/dossiers/nationalisation_arcelormittal"
	scrutin(arcelor, "17-9001", "l'article premier de la proposition de loi visant à la nationalisation d'ArcelorMittal France",
		arcelorSummary, prepareLawPhrase(arcelorSummary), deputies, true)
	require.Equal(t, 1, stancer.labels)
	require.Equal(t, 4, stancer.assigns, "one assignment per voter on a new subject")
	scrutin(arcelor, "17-9002", "l'amendement n° 96 de M. Bernhardt après l'article premier de la proposition de loi visant à la nationalisation d'ArcelorMittal France",
		"Prévoit la nationalisation d'ArcelorMittal France",
		"l'amendement n° 96 (UDR) visant à instaurer un audit préalable, déposé sur le texte de loi : « Prévoit la nationalisation d'ArcelorMittal France »", deputies, true)
	require.Equal(t, 1, stancer.labels, "the law's stored subject is reused")
	require.Equal(t, 4, stancer.assigns, "no assignment for a known subject")
	require.Equal(t, map[string]bool{"politique industrielle": true}, subjects("17-9002"))

	// Each voter has its 2 ballots in one stance, and the stance counts both.
	rows, err := storer.Pool().Query(ctx, `
		SELECT p.name, count(DISTINCT sdf.stance_document_id), count(*), max(sd.fact_count)
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		JOIN persons p ON p.id = fm.person_id
		JOIN stance_document_facts sdf ON sdf.fact_id = fs.fact_id
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE fs.metadata->>'law_key' IN ('17-9001', '17-9002')
		GROUP BY p.name`)
	require.NoError(t, err)
	voters := 0
	for rows.Next() {
		var name string
		var stances, ballots, factCount int
		require.NoError(t, rows.Scan(&name, &stances, &ballots, &factCount))
		require.Equal(t, 1, stances, "%s: one stance for both ballots", name)
		require.Equal(t, 2, ballots, name)
		require.Equal(t, 2, factCount, name)
		voters++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 4, voters)

	// Budget law: one label per scrutin, never the label of another scrutin.
	const plf = "https://an.test/dossiers/plf_2026"
	scrutin(plf, "17-9003", "l'amendement n° 2210 de Mme Pochon à l'article 12 du projet de loi de finances pour 2026",
		"Fixe le budget de l'État pour 2026", energyPhrase, deputies, true)
	scrutin(plf, "17-9004", "l'amendement n° 845 de M. Gassilloud à l'article 49 du projet de loi de finances pour 2026",
		"Fixe le budget de l'État pour 2026", defensePhrase, deputies[:2], false)
	require.Equal(t, 3, stancer.labels)
	require.Equal(t, 4+4+2, stancer.assigns, "another subject is assigned again")
	// A resumed scrutin reuses its stored label.
	scrutin(plf, "17-9004", "l'amendement n° 845 de M. Gassilloud à l'article 49 du projet de loi de finances pour 2026",
		"Fixe le budget de l'État pour 2026", defensePhrase, deputies[2:], true)
	require.Equal(t, 3, stancer.labels, "the scrutin's stored subject is reused")
	require.Equal(t, 4+4+2+2, stancer.assigns)
	require.Equal(t, map[string]bool{"fiscalité de l'énergie": true}, subjects("17-9003"))
	require.Equal(t, map[string]bool{"politique de défense": true}, subjects("17-9004"))

	requireEveryFactHasAStance(t, storer.Pool())
}

// TestVoteSubjectStancesPicksMostBallots: per person, the stance with the
// most ballots of the subject wins, then the lowest stance id; ballots of
// another subject do not count.
func TestVoteSubjectStancesPicksMostBallots(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	emb := make([]float32, 4096)
	emb[0] = 1
	n := 0
	// ballot stores a ballot fact with the vote subject subject.
	ballot := func(subject string) int64 {
		n++
		var factID int64
		require.NoError(t, storer.Pool().QueryRow(ctx, "INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", fmt.Sprintf("ballot %d", n)).Scan(&factID))
		_, err := storer.LinkSourceToFact(ctx, factID, fmt.Sprintf("https://an.test/%d", n), "q", nil, "assemblee_law_page", map[string]any{"vote_subject": subject})
		require.NoError(t, err)
		return factID
	}
	stance := func(personID int64, label string, facts ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{PersonID: personID, ClusterLabel: label, ClusterEmbedding: emb, Summary: "s", KeyFactIDs: facts, FactCount: len(facts)})
		require.NoError(t, err)
		for _, f := range facts {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}
	most, _, err := storer.AddPerson(ctx, "Subject Most")
	require.NoError(t, err)
	tie, _, err := storer.AddPerson(ctx, "Subject Tie")
	require.NoError(t, err)

	stance(most, "few", ballot("X"), ballot("Y"), ballot("Y"))
	mostWinner := stance(most, "many", ballot("X"), ballot("X"))
	tieWinner := stance(tie, "first", ballot("X"))
	stance(tie, "second", ballot("X"))

	got, err := store.VoteSubjectStances(ctx, "X")
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{most: mostWinner, tie: tieWinner}, got)
}

// requireEveryFactHasAStance fails the test when a stored fact is in no
// stance.
func requireEveryFactHasAStance(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var orphans int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM facts f
		WHERE NOT EXISTS (SELECT 1 FROM stance_document_facts sdf WHERE sdf.fact_id = f.id)`).Scan(&orphans))
	require.Zero(t, orphans, "every fact has a stance")
}
