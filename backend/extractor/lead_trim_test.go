package extractor

import "testing"

func TestStripLeadBeforeTitle(t *testing.T) {
	const title = "Réduction de nombre de fonctionnaires : le virage à 180 degrés de Gabriel Attal"
	ticker := "Interpellé par le sénateur PS Hussein Bourgi sur les violences lors de la manifestation du 24 octobre, le ministre de l'Intérieur Laurent Nuñez affirme qu'il n'y a eu « aucune complaisance ». "
	body := title + " Publié le Les rencontres économiques d'Aix-en-Provence sont souvent une bonne occasion."
	cases := []struct {
		name, content, title, want string
	}{
		{"ticker above the title", ticker + body, title, body},
		{"title with a site suffix", ticker + body, title + " - Public Sénat", body},
		{"title first: unchanged", body, title, body},
		{"no title in the text: unchanged", ticker, title, ticker},
		{"short title: unchanged", "Direct. " + ticker + "Direct", "Direct", "Direct. " + ticker + "Direct"},
	}
	for _, c := range cases {
		if got := stripLeadBeforeTitle(c.content, c.title); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	long := make([]byte, leadTrimMaxPrefix+10)
	for i := range long {
		long[i] = 'a'
	}
	if got := stripLeadBeforeTitle(string(long)+body, title); got != string(long)+body {
		t.Errorf("a prefix longer than %d must stay", leadTrimMaxPrefix)
	}
}
