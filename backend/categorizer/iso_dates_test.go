package categorizer

import "testing"

func TestISODatesInWords(t *testing.T) {
	cases := []struct{ text, lang, want string }{
		{"Bruno Retailleau a déclaré le 2025-03-03 que la France doit agir.", "fr", "Bruno Retailleau a déclaré le 3 mars 2025 que la France doit agir."},
		{"Stéphanie Rist a défendu la taxe (déclaration du 2025-11-01).", "fr", "Stéphanie Rist a défendu la taxe (déclaration du 1er novembre 2025)."},
		{"He said on 2026-10-28 that the plan holds.", "en", "He said on 28 October 2026 that the plan holds."},
		{"Le texte 2026-13-45 n'est pas une date.", "fr", "Le texte 2026-13-45 n'est pas une date."},
		{"Aucune date ici.", "fr", "Aucune date ici."},
	}
	for _, c := range cases {
		if got := isoDatesInWords(c.text, c.lang); got != c.want {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
	}
}
