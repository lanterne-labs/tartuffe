package util

import "strings"

// LooksFrench reports whether a text (typically a stance summary) already reads
// as French. Conservative: it leans toward "not French" on borderline text so
// English content is not mistaken for French.
func LooksFrench(text string) bool {
	s := strings.ToLower(text)
	if strings.ContainsAny(s, "éèàçêâîôûùïüœ") {
		return true
	}
	for _, m := range []string{" le ", " la ", " les ", " des ", " une ", " du ", " au ", " qu'", " d'", " l'", " a déclaré", " a affirmé", " a annoncé"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
