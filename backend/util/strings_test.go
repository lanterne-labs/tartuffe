package util

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short ascii untouched", "short", 10, "short"},
		{"exact length untouched", "abcdef", 6, "abcdef"},
		{"ascii cut keeps suffix within budget", "this is a very long string", 10, "this is..."},
		{"budget too small for suffix", "abcdef", 3, "..."},
		{"french text cut on a rune boundary", "Élisabeth Borne réaffirme l'âge légal à 64 ans", 20, "Élisabeth Borne r..."},
		{"multibyte text within budget untouched", "réforme des retraites", 21, "réforme des retraites"},
		{"cut right after an accented rune", "défi", 4, "défi"},
		{"cut inside accented run", "déjà vu, déjà fait", 8, "déjà ..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateString(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("TruncateString(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
			if n := utf8.RuneCountInString(got); n > tc.max && tc.max > 3 {
				t.Fatalf("result has %d runes, budget was %d", n, tc.max)
			}
		})
	}
}
