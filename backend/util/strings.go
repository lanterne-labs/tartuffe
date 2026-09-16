package util

import "unicode/utf8"

// TruncateString limits a string to maxLength runes, replacing the tail with
// "..." when it is cut. The cut happens on a rune boundary so multibyte text
// (accents, typographic punctuation) never yields a broken character.
func TruncateString(s string, maxLength int) string {
	if utf8.RuneCountInString(s) <= maxLength {
		return s
	}
	if maxLength <= 3 {
		return "..."
	}
	return string([]rune(s)[:maxLength-3]) + "..."
}
