package frpolitics

import (
	"strings"
	"testing"
	"time"
)

// TestStaleAmendmentSummary checks the detector on the stored summaries of
// amendment scrutins in prod (2026-11-01): those that describe the amendment
// or a part of the bill, and those that describe the bill.
func TestStaleAmendmentSummary(t *testing.T) {
	stale := []string{
		"Supprime l'article 3 du projet de loi d'urgence pour Mayotte.",
		"Supprime l'article 12 du projet de loi de financement de la sécurité sociale pour 2026.",
		"Rétablit des mesures pour répondre aux phénomènes troublant l'ordre public",
		"Supprime certaines dispositions du projet de loi de simplification de la vie économique.",
		"Supprime des dispositions du projet de loi de financement de la sécurité sociale pour 2025.",
		"Supprime un amendement relatif à la création d'un statut de l'élu local",
		"Modifie les mesures de réponse aux phénomènes troublant l'ordre public.",
		"Modifie le projet de loi sur la sécurité et l'ordre public",
		"Vise à supprimer l'article liminaire du projet de loi de financement de la sécurité sociale pour 2026.",
		"Étend le versement des allocations familiales dès le premier enfant, en modifiant l'article premier du texte.",
		"Étend les compétences du Parquet européen aux infractions à l'environnement (amendement n° 1 de M. Sitzenstuhl à la proposition de résolution).",
		"l'amendement de suppression n° 22 de M. Iordanoff et les amendements identiques suivants à l'article 23 bis a (examen prioritaire) de la proposition de loi",
		"Réinstaure l'article 4 supprimé concernant les mesures de sécurité et d'ordre public.",
	}
	for _, s := range stale {
		if !staleAmendmentSummary(s) {
			t.Errorf("stale summary not detected: %q", s)
		}
	}
	bill := []string{
		"Renforce les conditions d'accès à la nationalité française à Mayotte.",
		"Nationalise ArcelorMittal France afin de préserver la souveraineté industrielle de la France.",
		"Abroge les zones à faibles émissions-mobilités",
		"Modifie la définition pénale du viol et des agressions sexuelles",
		"Durcit la réponse pénale à l'égard des mineurs délinquants et renforce la responsabilité de leurs parents.",
		"Étend et pérennise progressivement l'expérimentation « territoires zéro chômeur de longue durée »",
		"Lève l'interdiction de recherche, d'exploration et d'exploitation des hydrocarbures dans les territoires d'outre-mer.",
	}
	for _, s := range bill {
		if staleAmendmentSummary(s) {
			t.Errorf("a summary of the bill flagged as stale: %q", s)
		}
	}
}

// TestUsableBillSummary checks that the repair keeps the old text when the
// new summary is doubtful or still about a part of the bill.
func TestUsableBillSummary(t *testing.T) {
	cases := []struct {
		sum  LawSummary
		want bool
	}{
		{LawSummary{SimpleStatement: "Renforce la lutte contre le narcotrafic", Confidence: 0.9}, true},
		{LawSummary{SimpleStatement: "Renforce la lutte contre le narcotrafic", Confidence: 0.3}, false},
		{LawSummary{SimpleStatement: "Supprime l'article 8 du texte contre le narcotrafic", Confidence: 0.9}, false},
		{LawSummary{SimpleStatement: "Le contenu précis de l'amendement n'est pas déterminable", Confidence: 0.9}, false},
		{LawSummary{SimpleStatement: " ", Confidence: 0.9}, false},
	}
	for _, c := range cases {
		if got := usableBillSummary(c.sum); got != c.want {
			t.Errorf("usableBillSummary(%+v) = %v, want %v", c.sum, got, c.want)
		}
	}
}

// TestSummaryQuoteInBallot checks that the quote the repair replaces is the
// one votedObjectPhrase writes, so a stored ballot carries it.
func TestSummaryQuoteInBallot(t *testing.T) {
	old := "Supprime l'article 3 du projet de loi d'urgence pour Mayotte."
	title := "l'amendement de suppression n° 120 de Mme Youssouffa à l'article 3 du projet de loi d'urgence pour Mayotte (première lecture)."
	phrase := votedObjectPhrase("l'amendement n° 120 (Dem) dont l'objet est : « Supprimer l'article. »", title, LawSummary{SimpleStatement: old})
	ballot := "Jean Dupont " + buildVoteStatement(VoteFor, phrase)
	if !strings.Contains(ballot, summaryQuote(old)) {
		t.Fatalf("ballot %q does not carry %q", ballot, summaryQuote(old))
	}
	newSum := "Crée des mesures d'urgence pour la reconstruction de Mayotte"
	got := strings.Replace(ballot, summaryQuote(old), summaryQuote(newSum), 1)
	want := "Jean Dupont a voté POUR l'amendement n° 120 (Dem) dont l'objet est : « Supprimer l'article. », déposé sur le texte de loi : « Crée des mesures d'urgence pour la reconstruction de Mayotte »"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestVoteDateSuffix checks that a ballot names the day of its scrutin, so
// two texts of one title voted a year apart read as two texts, and that the
// embedding text and a censure motion that names its date carry no suffix.
func TestVoteDateSuffix(t *testing.T) {
	d1 := time.Date(2024, 11, 6, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2025, 10, 28, 0, 0, 0, 0, time.UTC)
	phrase := "le texte de loi organique : « Reporte le renouvellement général du congrès et des assemblées de province de la Nouvelle-Calédonie »"
	ev := []VoteEvent{{DisplayName: "Alexandra Masson", Position: VoteFor}}
	a := deriveDeputyFacts(Law{Key: "17-264", VoteDate: &d1}, ev, LawSummary{}, phrase)
	b := deriveDeputyFacts(Law{Key: "17-3182", VoteDate: &d2}, ev, LawSummary{}, phrase)
	if !strings.HasSuffix(a[0].FactText, " (scrutin du 6 novembre 2024)") || !strings.HasSuffix(b[0].FactText, " (scrutin du 28 octobre 2025)") {
		t.Fatalf("texts without their date: %q / %q", a[0].FactText, b[0].FactText)
	}
	if a[0].EmbeddingText != phrase {
		t.Errorf("embedding text %q, want the object alone", a[0].EmbeddingText)
	}
	if got := deriveDeputyFacts(Law{Key: "17-1"}, ev, LawSummary{}, phrase)[0].FactText; strings.Contains(got, "scrutin du") {
		t.Errorf("an undated scrutin got a date: %q", got)
	}
	censure := censurePrefix + " du 6 novembre 2024"
	if got := voteDateSuffix(Law{VoteDate: &d1}, censure); got != "" {
		t.Errorf("a censure motion that names its date got %q", got)
	}
	organes := map[string]OrganeInfo{"PO1": {OrganeRef: "PO1", ShortName: "RN", LongName: "Rassemblement National"}}
	party := derivePartyMajorityFacts(Law{Key: "17-264", VoteDate: &d1}, LawGroupStats{"PO1": {Position: VoteFor, For: 100, Members: 120}}, 0.5, organes, LawSummary{}, phrase)
	if len(party) != 1 || !strings.HasSuffix(party[0].FactText, " (scrutin du 6 novembre 2024)") {
		t.Errorf("party ballot without its date: %+v", party)
	}
}
