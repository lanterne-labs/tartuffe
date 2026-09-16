package util

import "testing"

func TestLooksFrench(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"He supports the reform of the pension system.", false},
		{"Il soutient la réforme des retraites.", true},          // accents
		{"Il soutient la reforme.", true},                        // article " la "
		{"The minister said that le pen would run.", true},       // " le " anywhere (conservative by design)
		{"Position on immigration and border control", false},    // no article, no accent
		{"Supports the plan; qu'il faut agir", true},             // " qu'"
		{"Contractions like don't and can't are English", false}, // apostrophes without French markers
		{"Elle a déclaré vouloir baisser les impôts", true},      // " a déclaré"
		{"Latin phrase: status quo", false},                      // no French markers
		{"The du Pont company announced a merger", true},         // " du " triggers (known conservative false positive)
	}
	for _, c := range cases {
		if got := LooksFrench(c.in); got != c.want {
			t.Errorf("LooksFrench(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
