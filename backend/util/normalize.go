package util

import "strings"

// NormalizeString applies basic sanitization: trim whitespace and convert to lowercase.
// Renamed from normalizePersonName for more general use.
func NormalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// personPunct folds the typographic variants of ASCII punctuation that show up in
// extracted names (curly apostrophes/quotes, en/em dashes) to their ASCII form, so
// "d'azur" and "d'azur", or "jean–luc" and "jean-luc", canonicalize identically.
var personPunct = strings.NewReplacer(
	"’", "'", "‘", "'", "ʼ", "'", "′", "'", // ’ ‘ ʼ ′ -> '
	"“", "\"", "”", "\"", "„", "\"", // “ ” „ -> "
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-", "−", "-", // dashes -> -
)

// NormalizePersonName is the single canonical form for PERSON names: lowercase,
// accent-stripped (é→e), typographic punctuation folded to ASCII, and internal
// whitespace collapsed to single spaces. Person rows are stored and matched under
// this form so NO surface variant (accents, curly apostrophes, fancy dashes, extra
// spaces) can ever split one person into two rows. It is idempotent.
// Only use for persons — NormalizeString stays variant-preserving for everything else.
func NormalizePersonName(s string) string {
	s = RemoveAccents(NormalizeString(s))
	s = personPunct.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// PersonNameKey is NormalizePersonName with every separator removed: spaces,
// ASCII punctuation and symbols, the soft hyphen and the zero-width marks.
// Letters (of any script) and digits stay. Two names with the same key differ
// only by where a word was cut or joined ("olivier fa-ure", "jean- luc",
// "marionmarechal_"), so they name one person. The SQL function
// person_name_key folds the stored names the same way. A name made only of
// separators has an empty key.
func PersonNameKey(s string) string {
	var b strings.Builder
	for _, r := range NormalizePersonName(s) {
		if nameSeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func nameSeparator(r rune) bool {
	if r < 0x80 {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}
	return r == 0xAD || r >= 0x200B && r <= 0x200F
}
