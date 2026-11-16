package frpolitics

import (
	"testing"
	"time"
)

func TestRedateBallotText(t *testing.T) {
	d := time.Date(2025, 12, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, in, want string
	}{
		{"suffix added",
			"Jean Dupont a voté POUR le texte de loi : « Renforce la protection des mineurs »",
			"Jean Dupont a voté POUR le texte de loi : « Renforce la protection des mineurs » (scrutin du 17 décembre 2025)"},
		{"already suffixed is untouched",
			"Jean Dupont a voté CONTRE le texte de loi : « X » (scrutin du 17 décembre 2025)",
			"Jean Dupont a voté CONTRE le texte de loi : « X » (scrutin du 17 décembre 2025)"},
		{"abstention, the masculine past participle",
			"Marie Durand s'est abstenu lors du vote sur la déclaration du Gouvernement",
			"Marie Durand a choisi l'abstention lors du vote sur la déclaration du Gouvernement (scrutin du 17 décembre 2025)"},
		{"abstention, the feminine past participle",
			"Marie Durand s'est abstenue lors du vote sur le texte de loi : « X »",
			"Marie Durand a choisi l'abstention lors du vote sur le texte de loi : « X » (scrutin du 17 décembre 2025)"},
		{"collective abstention",
			"Les Écologistes s'est collectivement abstenu lors du vote sur le texte de loi : « X »",
			"Les Écologistes a collectivement choisi l'abstention lors du vote sur le texte de loi : « X » (scrutin du 17 décembre 2025)"},
		{"old abstention on a suffixed ballot: only the wording changes",
			"Marie Durand s'est abstenu lors du vote sur « X » (scrutin du 17 décembre 2025)",
			"Marie Durand a choisi l'abstention lors du vote sur « X » (scrutin du 17 décembre 2025)"},
		{"a text naming the date already (a censure motion) gets no suffix",
			"Jean Dupont a voté POUR la motion de censure du 17 décembre 2025",
			"Jean Dupont a voté POUR la motion de censure du 17 décembre 2025"},
		{"the first of the month is 1er",
			"Jean Dupont a voté POUR « X »",
			"Jean Dupont a voté POUR « X » (scrutin du 1er décembre 2025)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			date := d
			if c.name == "the first of the month is 1er" {
				date = time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
			}
			got := redateBallotText(c.in, date)
			if got != c.want {
				t.Fatalf("\n got: %s\nwant: %s", got, c.want)
			}
			if again := redateBallotText(got, date); again != got {
				t.Fatalf("not idempotent:\n once: %s\ntwice: %s", got, again)
			}
		})
	}
}

// The wording redate writes is the wording the current builders write.
func TestRedateMatchesTheCurrentBuilders(t *testing.T) {
	d := time.Date(2025, 12, 17, 0, 0, 0, 0, time.UTC)
	law := Law{VoteDate: &d}
	obj := "le texte de loi : « X »"
	want := "Marie Durand " + buildVoteStatement(VoteAbstention, obj) + voteDateSuffix(law, obj)
	if got := redateBallotText("Marie Durand s'est abstenu lors du vote sur "+obj, d); got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
	want = "Les Écologistes " + buildCollectiveVoteStatement(VoteAbstention, obj) + voteDateSuffix(law, obj)
	if got := redateBallotText("Les Écologistes s'est collectivement abstenu lors du vote sur "+obj, d); got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
	want = "Jean Dupont " + buildVoteStatement(VoteFor, obj) + voteDateSuffix(law, obj)
	if got := redateBallotText("Jean Dupont "+buildVoteStatement(VoteFor, obj), d); got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

// A ballot stored before textNoun calls a resolution « le texte de loi »
// (prod scrutins 17-3260, 17-5225, 17-6517, audit of 2026-11-16).
func TestRenounBallotText(t *testing.T) {
	res := "la proposition de résolution visant à dénoncer les accords franco-algériens du 27 décembre 1968 (article 34-1 de la Constitution)."
	cases := []struct {
		name, title, in, want string
	}{
		{"the resolution alone", res,
			"Jean Dupont a voté POUR le texte de loi : « Demande au gouvernement de dénoncer les accords » (scrutin du 30 octobre 2025)",
			"Jean Dupont a voté POUR la proposition de résolution : « Demande au gouvernement de dénoncer les accords » (scrutin du 30 octobre 2025)"},
		{"an abstention", res,
			"Marie Durand a choisi l'abstention lors du vote sur le texte de loi : « X »",
			"Marie Durand a choisi l'abstention lors du vote sur la proposition de résolution : « X »"},
		{"its single article", "l'article unique de la proposition de résolution tendant à la création d'une commission d'enquête",
			"Jean Dupont a voté POUR l'article unique (« Crée une commission d'enquête ») du texte de loi : « X »",
			"Jean Dupont a voté POUR l'article unique (« Crée une commission d'enquête ») de la proposition de résolution : « X »"},
		{"an amendment to it", "l'amendement n° 20 de Mme Le Grip à l'article unique de la proposition de résolution européenne",
			"Jean Dupont a voté CONTRE l'amendement n° 20 (EPR) (« Supprime X »), déposé sur le texte de loi : « Y »",
			"Jean Dupont a voté CONTRE l'amendement n° 20 (EPR) (« Supprime X »), déposé sur la proposition de résolution : « Y »"},
		{"an organic law's article", "l'article 2 du projet de loi organique relatif à X",
			"Jean Dupont a voté POUR l'article 2 (« Z ») du texte de loi : « X »",
			"Jean Dupont a voté POUR l'article 2 (« Z ») du texte de loi organique : « X »"},
		{"a law is untouched", "l'ensemble de la proposition de loi visant à X",
			"Jean Dupont a voté POUR le texte de loi : « X »",
			"Jean Dupont a voté POUR le texte de loi : « X »"},
		{"a ballot without a title is untouched", "",
			"Jean Dupont a voté POUR le texte de loi : « X »",
			"Jean Dupont a voté POUR le texte de loi : « X »"},
		{"a summary that quotes « le texte de loi » later is not the noun", res,
			"Jean Dupont a voté POUR la proposition de résolution : « Demande que le texte de loi : « X » soit abrogé »",
			"Jean Dupont a voté POUR la proposition de résolution : « Demande que le texte de loi : « X » soit abrogé »"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renounBallotText(c.in, c.title)
			if got != c.want {
				t.Fatalf("\n got: %s\nwant: %s", got, c.want)
			}
			if again := renounBallotText(got, c.title); again != got {
				t.Fatalf("not idempotent:\n once: %s\ntwice: %s", got, again)
			}
		})
	}
}

// What renoun writes is what the current builders write for the title.
func TestRenounMatchesTheCurrentBuilders(t *testing.T) {
	title := "l'article unique de la proposition de résolution tendant à X"
	sum := LawSummary{SimpleStatement: "Crée une commission d'enquête sur X"}
	old := "Jean Dupont a voté POUR l'article unique du texte de loi : « Crée une commission d'enquête sur X »"
	want := "Jean Dupont a voté POUR " + votedObjectPhrase("l'article unique", title, sum)
	if got := renounBallotText(old, title); got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}
