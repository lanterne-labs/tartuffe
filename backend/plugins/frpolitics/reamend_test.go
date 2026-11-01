package frpolitics

import (
	"regexp"
	"testing"
)

// TestTruncatedAmendmentRe checks the pattern ReamendBallots rewrites with on
// the two prod shapes of 2026-11-01 (an amendment of a group, a
// sous-amendement), and that a closed object is left alone.
func TestTruncatedAmendmentRe(t *testing.T) {
	re := regexp.MustCompile(TruncatedAmendmentRe)
	object := "l'amendement n° 15 (EcoS) dont l'objet est : « Par cet amendement de repli, le groupe écologiste et social souhaite supprimer l'obligation… »"
	cases := []struct{ in, want string }{
		{
			"UDR a collectivement voté CONTRE l'amendement n° 15 (EcoS) dont l'objet est : « Par cet amendement de repli, le groupe écologiste et social souhaite supprimer l'obligation faite aux futurs époux. À travers …, déposé sur le texte de loi : « Renforce les prérogatives. »",
			"UDR a collectivement voté CONTRE " + object + ", déposé sur le texte de loi : « Renforce les prérogatives. »",
		},
		{
			"Bénédicte Auzanot a voté POUR le sous-amendement n° 54 (RN) dont l'objet est : « La formation spécialisée doit intégrer le consentement aux soins du …, déposé sur le texte de loi : « Étend la prise en charge »",
			"Bénédicte Auzanot a voté POUR " + object + ", déposé sur le texte de loi : « Étend la prise en charge »",
		},
	}
	for _, c := range cases {
		if got := re.ReplaceAllLiteralString(c.in, object+", déposé sur"); got != c.want {
			t.Errorf("got %q\nwant %q", got, c.want)
		}
	}
	closed := "Jean Dupont a voté POUR l'amendement n° 3 (RN) dont l'objet est : « Supprimer l'article. », déposé sur le texte de loi : « X »"
	if re.MatchString(closed) {
		t.Errorf("a closed object must not match: %q", closed)
	}
}
