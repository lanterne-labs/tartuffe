package categorizer

import "testing"

func TestNamedInSource(t *testing.T) {
	cases := []struct {
		name, url, title, content string
		want                      bool
	}{
		{"nicolas dupont-aignan", "https://www.lopinion.fr/politique/philippe-juvin-lr-pourquoi-je-soutiens-bruno-retailleau",
			"Philippe Juvin (LR) : « Pourquoi je soutiens Bruno Retailleau »", "Je soutiens Bruno Retailleau parce qu'il a fait le choix de devenir ministre.", false},
		{"nicolas dupont-aignan", "https://www.instagram.com/p/DKhES-_iN_u/", "", "En Commission Affaires Étrangères, je me suis opposée aux rapports.", false},
		{"nicolas dupont-aignan", "https://www.sudradio.fr/x", "Dupont-Aignan : \"Les règles de l'UE une prison\"", "…", true},
		{"marine le pen", "https://example.org/a", "", "La candidate RN Marine Le Pen a confirmé.", true},
		{"marine le pen", "https://example.org/a", "", "Il a parlé pendant une heure.", false},
		{"françois ruffin", "https://francoisruffin.fr/mepris-de-chasse/", "Mépris de chasse", "La nature doit faire l'objet d'un partage.", true},
		{"jean-luc mélenchon", "https://example.org/a", "", "Jean-Luc MÉLENCHON a répondu.", true},
	}
	for _, c := range cases {
		if got := NamedInSource(c.name, c.url, c.title, c.content); got != c.want {
			t.Errorf("NamedInSource(%q, %q) = %v, want %v", c.name, c.url, got, c.want)
		}
	}
}
