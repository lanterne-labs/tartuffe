package util

import "strings"

// NormalizeString applies basic sanitization: trim whitespace and convert to lowercase.
// Renamed from normalizePersonName for more general use.
func NormalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
} 