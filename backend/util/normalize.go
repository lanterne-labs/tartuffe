package util

import "strings"

// NormalizeString applies basic sanitization: trim whitespace and convert to lowercase.
// Renamed from normalizePersonName for more general use.
func NormalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizePersonName is the canonical form for PERSON names: lowercase, trimmed,
// and ACCENT-STRIPPED (é→e). Person rows are stored and matched under this form so
// accent variants ("éric ciotti" vs "eric ciotti") can never become two rows.
// Only use for persons — NormalizeString stays accent-preserving for everything else.
func NormalizePersonName(s string) string {
	return RemoveAccents(NormalizeString(s))
}
