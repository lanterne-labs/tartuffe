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
