package frpolitics

import "testing"

func TestSameAuthor(t *testing.T) {
	plm := "l'amendement n° 2 de M. Sitzenstuhl et les amendements identiques suivants à l'article premier de la proposition de loi visant à réformer le mode d'élection du Conseil de Paris"
	cases := []struct {
		title, signatories string
		want               bool
	}{
		{plm, "M. Sitzenstuhl, Mme Dupont", true},
		{plm, "Mme Calvez, M. Balanant", false}, // amendment n° 2 of another bill, sorted the same day
		{plm, "", true},                         // nothing to compare
		{"l'amendement n° 30 de Mme Lebec à l'article 2", "Mme Lebec", true},
		{"le sous-amendement n° 372 de M. Verny à l'amendement n° 12", "M. Vérny", true},
		{"l'amendement n° 14 de Mme Poussier-Winsback après l'article 3", "Mme Poussier Winsback et M. Fait", true},
		{"l'amendement n° 7 du Gouvernement à l'article 1er", "Gouvernement", true},
		{"l'amendement n° 7 de la commission des lois", "M. Houlié", true},
	}
	for _, c := range cases {
		if got := sameAuthor(c.title, c.signatories); got != c.want {
			t.Errorf("sameAuthor(%q, %q) = %v, want %v", c.title, c.signatories, got, c.want)
		}
	}
}
