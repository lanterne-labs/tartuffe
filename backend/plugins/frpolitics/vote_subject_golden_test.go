package frpolitics

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"digital-mirror/backend/core"
	"digital-mirror/backend/llmtest"
)

// memSubjects is a voteSubjectStore in memory: the vote subject stored per
// scope, and no stance yet.
type memSubjects struct {
	mu       sync.Mutex
	subjects map[subjectScope]string
}

func (m *memSubjects) StoredVoteSubject(_ context.Context, scope subjectScope) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subjects[scope], nil
}

func (m *memSubjects) VoteSubjectStances(context.Context, string) (map[int64]int64, error) {
	return nil, nil
}

// store records subject on the ballots of scope, as the insert does.
func (m *memSubjects) store(scope subjectScope, subject string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subjects[scope]; !ok {
		m.subjects[scope] = subject
	}
}

// labelCounter counts the SubjectLabel calls of the real stance manager.
type labelCounter struct {
	*core.StanceManager
	mu    sync.Mutex
	calls int
}

func (c *labelCounter) SubjectLabel(ctx context.Context, text string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.StanceManager.SubjectLabel(ctx, text)
}

// subjectCase is one law with the scrutins of the case, in vote order.
type subjectCase struct {
	name     string
	budget   bool
	dossier  string
	scrutins []FactRecord // one ballot per scrutin: LawKey, LawTitle, LawSummary, EmbeddingText
}

// voteSubjectCases are prod-shaped scrutins. The bill summary is made per
// scrutin, so two scrutins on one bill word it differently. The amendments
// n° 40, 7 and 1494 and the bill summaries are prod texts; the ArcelorMittal
// and budget amendments have the shape of the plugin's object phrases.
var voteSubjectCases = []subjectCase{
	{name: "exécution des peines", dossier: "https://an.test/dossiers/execution_peines", scrutins: []FactRecord{
		{LawKey: "17-1", LawTitle: "l'amendement n° 40 de M. Rambaud à l'article 2 de la proposition de loi visant à assurer l'exécution des peines d'emprisonnement ferme",
			LawSummary:    "Assure l'exécution des peines d'emprisonnement ferme",
			EmbeddingText: "l'amendement n° 40 (RN) dont l'objet est : « Le présent amendement vise à supprimer les aménagements de peine ab initio et soumettre l’aménagement à l’exécution minimale du tiers de la peine ferme prononcée. », déposé sur le texte de loi : « Assure l'exécution des peines d'emprisonnement ferme »"},
		{LawKey: "17-2", LawTitle: "l'ensemble de la proposition de loi visant à assurer l'exécution des peines d'emprisonnement ferme (première lecture)",
			LawSummary: "Améliore l'exécution des peines d'emprisonnement ferme"},
	}},
	{name: "nationalité à Mayotte", dossier: "https://an.test/dossiers/nationalite_mayotte", scrutins: []FactRecord{
		{LawKey: "17-3", LawTitle: "l'amendement n° 7 de M. Rancoule avant l'article premier de la proposition de loi visant à renforcer les conditions d'accès à la nationalité française à Mayotte",
			LawSummary:    "Renforce les conditions d'accès à la nationalité française à Mayotte.",
			EmbeddingText: "l'amendement n° 7 (RN) dont l'objet est : « Le Rassemblement National propose l'abrogation du droit du sol sur l'ensemble du territoire de la République. Dans le cadre de la présente proposition de loi qui vise spécifiquement Mayotte, il en propose l'application…, déposé sur le texte de loi : « Renforce les conditions d'accès à la nationalité française à Mayotte. »"},
		{LawKey: "17-4", LawTitle: "l'ensemble de la proposition de loi visant à renforcer les conditions d'accès à la nationalité française à Mayotte (première lecture)",
			LawSummary: "Durcit l'accès à la nationalité française à Mayotte."},
	}},
	{name: "nationalisation d'ArcelorMittal", dossier: "https://an.test/dossiers/nationalisation_arcelormittal", scrutins: []FactRecord{
		{LawKey: "17-5", LawTitle: "le sous-amendement n° 372 de M. Verny à l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France",
			LawSummary:    "Prévoit la nationalisation d'ArcelorMittal France",
			EmbeddingText: "le sous-amendement n° 372 (UDR) visant à porter de six mois à un an le délai de l'audit de la Cour des comptes préalable à la nationalisation, déposé sur le texte de loi : « Prévoit la nationalisation d'ArcelorMittal France »"},
		{LawKey: "17-6", LawTitle: "l'ensemble de la proposition de loi visant à la nationalisation d'ArcelorMittal France afin de préserver la souveraineté industrielle de la France (première lecture).",
			LawSummary: "Nationalise ArcelorMittal France pour préserver la souveraineté industrielle"},
	}},
	{name: "PLFSS 2025", budget: true, dossier: "https://an.test/dossiers/plfss_2025", scrutins: []FactRecord{
		{LawKey: "17-7", LawTitle: "l'amendement n° 1494 de M. Coquerel à l'article 6 du projet de loi de financement de la sécurité sociale pour 2025",
			EmbeddingText: "l'amendement n° 1494 (LFI-NFP) dont l'objet est : « Cet amendement vise à supprimer l'article prévoyant la pérennisation du dispositif d'exonération de cotisations patronales TO-DE (travailleurs occasionnels et demandeurs d'emplois). Ce dispositif a été une réponse à la…, déposé sur le texte de loi : « Supprime un amendement relatif au projet de loi de financement de la sécurité sociale 2025 »"},
	}},
	{name: "PLF 2026", budget: true, dossier: "https://an.test/dossiers/plf_2026", scrutins: []FactRecord{
		{LawKey: "17-8", LawTitle: "l'amendement n° 2210 de Mme Pochon après l'article 12 du projet de loi de finances pour 2026",
			EmbeddingText: "l'amendement n° 2210 (EcoS) visant à créer une contribution exceptionnelle sur les bénéfices des grandes entreprises du secteur de l'énergie, déposé sur le texte de loi : « Fixe les recettes et les dépenses de l'État pour l'année 2026 »"},
		{LawKey: "17-9", LawTitle: "l'amendement n° 845 de M. Gassilloud à l'article 49 du projet de loi de finances pour 2026",
			EmbeddingText: "l'amendement n° 845 (DR) visant à augmenter de 300 millions d'euros les crédits de la mission « Défense » pour l'équipement des forces, déposé sur le texte de loi : « Fixe les recettes et les dépenses de l'État pour l'année 2026 »"},
		{LawKey: "17-10", LawTitle: "l'amendement n° 1302 de M. Vallaud à l'article 49 du projet de loi de finances pour 2026",
			EmbeddingText: "l'amendement n° 1302 (SOC) visant à rétablir les crédits de MaPrimeRénov' pour la rénovation énergétique des logements, déposé sur le texte de loi : « Fixe les recettes et les dépenses de l'État pour l'année 2026 »"},
	}},
}

// TestVoteSubjectGolden measures the vote subjects that the placement relies
// on, through newVotePlacer with the real SubjectLabel:
//   - an ordinary law gets one SubjectLabel call; its later scrutins reuse the
//     stored subject, so all its ballots share one subject;
//   - ordinary laws on different subjects get different labels, so the
//     lookup does not put the ballots of two laws in one stance;
//   - the scrutins of a budget law get one label each, and amendments on
//     different subjects get different labels.
//
// LOCAL ONLY: it skips without a model (llmtest.Setup).
//
//	go test -v -run TestVoteSubjectGolden ./backend/plugins/frpolitics/
func TestVoteSubjectGolden(t *testing.T) {
	llmtest.Setup(t)
	sm, err := core.NewStanceManagerFromEnv(nil)
	if err != nil {
		t.Fatalf("failed to create stance manager: %v", err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	store := &memSubjects{subjects: map[subjectScope]string{}}
	var mu sync.Mutex
	subjects := make([][]string, len(voteSubjectCases)) // per case, per scrutin
	calls := make([]int, len(voteSubjectCases))
	llmtest.Group(t, func(t *testing.T) {
		for i, c := range voteSubjectCases {
			i, c := i, c
			t.Run(c.name, func(t *testing.T) {
				llmtest.Parallel(t)
				counter := &labelCounter{StanceManager: sm}
				var got []string
				for _, rec := range c.scrutins {
					rec.SourceURL = c.dossier
					if rec.EmbeddingText == "" {
						rec.EmbeddingText = prepareLawPhrase(rec.LawSummary)
					}
					if isBudgetLaw(rec.LawTitle) != c.budget {
						t.Fatalf("isBudgetLaw(%.60q) = %v, want %v", rec.LawTitle, !c.budget, c.budget)
					}
					p, err := newVotePlacer(ctx, store, counter, rec)
					if err != nil {
						t.Fatalf("scrutin %s: %v", rec.LawKey, err)
					}
					scope, _, err := voteSubjectOf(rec)
					if err != nil {
						t.Fatal(err)
					}
					store.store(scope, p.subject)
					got = append(got, p.subject)
					t.Logf("%s %q ← %.80s", rec.LawKey, p.subject, rec.EmbeddingText)
				}
				mu.Lock()
				subjects[i], calls[i] = got, counter.calls
				mu.Unlock()
			})
		}
	})

	passed, total := 0, 0
	check := func(ok bool, format string, args ...any) {
		total++
		if !ok {
			t.Errorf(format, args...)
			return
		}
		passed++
	}
	var ordinary, budget []string
	for i, c := range voteSubjectCases {
		if c.budget {
			check(calls[i] == len(c.scrutins), "%s: %d label calls, want one per scrutin (%d)", c.name, calls[i], len(c.scrutins))
			budget = append(budget, subjects[i]...)
			continue
		}
		same := len(subjects[i]) == len(c.scrutins)
		for _, s := range subjects[i] {
			same = same && s == subjects[i][0]
		}
		check(calls[i] == 1 && same, "%s: %d label calls and subjects %q, want 1 call and one subject", c.name, calls[i], subjects[i])
		if len(subjects[i]) > 0 {
			ordinary = append(ordinary, subjects[i][0])
		}
	}
	distinct := func(kind string, labels []string) {
		seen := map[string]bool{}
		for _, l := range labels {
			check(l != "" && !seen[l], "%s on different subjects share label %q (all: %q)", kind, l, labels)
			seen[l] = true
		}
	}
	distinct("ordinary laws", ordinary)
	distinct("budget-law scrutins", budget)
	fmt.Fprintf(os.Stderr, "\n=== VOTE SUBJECT GOLDEN: %d/%d passed (%d failed) ===\n", passed, total, total-passed)
}
