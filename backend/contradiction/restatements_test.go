package contradiction

import (
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
)

// A statement standing for a group renders its other wordings on one line
// after the headline, and a plain statement renders as before.
func TestStatementRender(t *testing.T) {
	plain := promptrules.Statement{Text: "Il veut réduire le déficit."}.Render()
	if plain != `"Il veut réduire le déficit."` {
		t.Fatalf("plain statement = %s", plain)
	}
	group := promptrules.Statement{Text: "un plan", Also: []string{"43,8 milliards", "cinq ans"}}.Render()
	want := "\"un plan\"\n  Other wordings of the same statement: \"43,8 milliards\" / \"cinq ans\""
	if group != want {
		t.Fatalf("group statement =\n%s\nwant\n%s", group, want)
	}
}

// The detector judges a group with all of its wordings: a reversal whose
// detail exists only in a restatement is found, and the wordings of one
// statement are never judged against each other. Majority of 3 per case,
// like the other judge goldens. Local-only (live LLM).
func TestDetectorJudgesGroupsWithTheirRestatements(t *testing.T) {
	llmtest.Setup(t)
	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pair FactPair
		want bool
	}{
		{
			name: "detail_only_in_a_restatement",
			pair: FactPair{
				TextA: "François Bayrou a proposé un plan pour ramener le déficit sous un certain niveau afin de sortir du surendettement.",
				AlsoA: []string{
					"François Bayrou a proposé un effort de 43,8 milliards d'euros, incluant la suppression de deux jours fériés, pour ramener le déficit sous les 5 % du PIB.",
					"François Bayrou a prôné la définition d'un plan de cinq ans pour ramener les déficits en dessous du niveau moyen de la croissance afin de sortir du surendettement.",
				},
				DateA: &date,
				TextB: "François Bayrou a affirmé qu'il ne supprimerait aucun jour férié.",
				DateB: &date,
			},
			want: true,
		},
		{
			name: "restatements_disagree_among_themselves",
			pair: FactPair{
				TextA: "Raphaël Glucksmann a proposé de revaloriser le chèque énergie pour les classes populaires et moyennes.",
				AlsoA: []string{
					"Raphaël Glucksmann a proposé de tripler le chèque énergie à 200 euros pour les classes populaires et moyennes.",
					"Raphaël Glucksmann propose que le montant du chèque énergie soit porté de 150 euros à 500 euros par an.",
				},
				DateA: &date,
				TextB: "Raphaël Glucksmann a proposé de financer les aides à l'énergie par la taxation des superprofits des multinationales pétrolières.",
				DateB: &date,
			},
			want: false,
		},
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yes := 0
			for run := 0; run < 3; run++ {
				r := detector.AreContradictoryBatch(ctx, []FactPair{tc.pair})[0]
				if r.Error != nil {
					t.Fatalf("run %d: %v", run+1, r.Error)
				}
				if r.IsContradictory {
					yes++
				}
				t.Logf("run %d: contradictory=%v reason=%s", run+1, r.IsContradictory, strings.TrimSpace(r.Reason))
			}
			if got := yes >= 2; got != tc.want {
				t.Fatalf("majority verdict = %v (%d/3 yes), want %v", got, yes, tc.want)
			}
		})
	}

	// The reason the change exists: the same Bayrou pair without its
	// restatements. Logged, not asserted — it documents what the judge used
	// to see.
	vague := cases[0].pair
	vague.AlsoA = nil
	r := detector.AreContradictoryBatch(ctx, []FactPair{vague})[0]
	t.Logf("headline only (before): contradictory=%v reason=%s", r.IsContradictory, strings.TrimSpace(r.Reason))
}
