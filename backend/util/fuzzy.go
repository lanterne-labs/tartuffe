package util

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// RemoveAccents removes diacritics from a string.
func RemoveAccents(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)
	return result
}

// NormalizeForFuzzy normalizes a string for fuzzy matching:
// - Lowercase
// - Remove accents
// - Remove common French articles and prepositions
// - Remove extra whitespace
func NormalizeForFuzzy(s string) string {
	// Lowercase
	s = strings.ToLower(s)

	// Remove accents
	s = RemoveAccents(s)

	// Split into words
	words := strings.Fields(s)

	// Remove common French articles/prepositions
	commonWords := map[string]bool{
		"le": true, "la": true, "les": true,
		"de": true, "du": true, "des": true,
		"d": true, "l": true,
	}

	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if !commonWords[word] {
			filtered = append(filtered, word)
		}
	}

	return strings.Join(filtered, " ")
}

// levenshteinDistance calculates the Levenshtein distance between two strings.
func levenshteinDistance(s1, s2 string) int {
	if s1 == s2 {
		return 0
	}

	r1 := []rune(s1)
	r2 := []rune(s2)

	if len(r1) == 0 {
		return len(r2)
	}
	if len(r2) == 0 {
		return len(r1)
	}

	// Create distance matrix
	matrix := make([][]int, len(r1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(r2)+1)
	}

	// Initialize first row and column
	for i := 0; i <= len(r1); i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len(r2); j++ {
		matrix[0][j] = j
	}

	// Fill matrix
	for i := 1; i <= len(r1); i++ {
		for j := 1; j <= len(r2); j++ {
			cost := 1
			if r1[i-1] == r2[j-1] {
				cost = 0
			}

			matrix[i][j] = min(
				matrix[i-1][j]+1,      // deletion
				matrix[i][j-1]+1,      // insertion
				matrix[i-1][j-1]+cost, // substitution
			)
		}
	}

	return matrix[len(r1)][len(r2)]
}

// similarityScore calculates a normalized similarity score (0.0 to 1.0) between two strings.
// Higher scores indicate more similar strings.
// Handles exact matches, substring matches, acronyms, and fuzzy matches.
func similarityScore(s1, s2 string) float64 {
	// Normalize both strings
	n1 := NormalizeForFuzzy(s1)
	n2 := NormalizeForFuzzy(s2)

	if n1 == n2 {
		return 1.0
	}

	// Check for word-level match (query matches a complete word in candidate)
	words1 := strings.Fields(n1)
	words2 := strings.Fields(n2)

	// If one is a single word, check if it matches any word in the other
	if len(words1) == 1 {
		for _, word := range words2 {
			if word == words1[0] {
				return 0.8 // High score for exact word match
			}
		}
	}
	if len(words2) == 1 {
		for _, word := range words1 {
			if word == words2[0] {
				return 0.8 // High score for exact word match
			}
		}
	}

	// Check for substring match (one string contains the other)
	if strings.Contains(n1, n2) || strings.Contains(n2, n1) {
		// Calculate overlap ratio
		shorter := len(n1)
		if len(n2) < shorter {
			shorter = len(n2)
		}
		longer := len(n1)
		if len(n2) > longer {
			longer = len(n2)
		}
		if longer == 0 {
			return 0.0
		}
		return float64(shorter) / float64(longer)
	}

	// Check for acronym match
	// Try matching shorter string against acronym of longer string
	if len(words1) > 1 && len(n2) <= len(words1) {
		acronym := ""
		for _, word := range words1 {
			if len(word) > 0 {
				acronym += string(word[0])
			}
		}
		if acronym == n2 {
			return 0.9 // High score for acronym match
		}
	}
	if len(words2) > 1 && len(n1) <= len(words2) {
		acronym := ""
		for _, word := range words2 {
			if len(word) > 0 {
				acronym += string(word[0])
			}
		}
		if acronym == n1 {
			return 0.9 // High score for acronym match
		}
	}

	// Fall back to Levenshtein distance for fuzzy matching
	distance := levenshteinDistance(n1, n2)
	maxLen := len(n1)
	if len(n2) > maxLen {
		maxLen = len(n2)
	}

	if maxLen == 0 {
		return 0.0
	}

	return 1.0 - float64(distance)/float64(maxLen)
}

// NormalizeToDirName converts a name to a directory-safe string.
// Example: "Marine Le Pen" -> "marine_le_pen"
// Example: "Éric Ciotti" -> "eric_ciotti"
func NormalizeToDirName(name string) string {
	// Lowercase
	s := strings.ToLower(name)

	// Remove accents
	s = RemoveAccents(s)

	// Replace spaces and non-alphanumeric chars with underscores
	var result strings.Builder
	prevUnderscore := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			result.WriteRune('_')
			prevUnderscore = true
		}
	}

	// Trim leading/trailing underscores
	return strings.Trim(result.String(), "_")
}

// PersonMatch represents a matched person with their similarity score.
type PersonMatch struct {
	ID         int64
	Name       string
	EntityType string
	Score      float64
}

// FindBestMatches finds the best matching persons for a query string.
// Returns matches sorted by score (highest first).
func FindBestMatches(query string, candidates []struct {
	ID         int64
	Name       string
	EntityType string
}, threshold float64, maxResults int) []PersonMatch {
	matches := make([]PersonMatch, 0)

	for _, candidate := range candidates {
		score := similarityScore(query, candidate.Name)
		if score >= threshold {
			matches = append(matches, PersonMatch{
				ID:         candidate.ID,
				Name:       candidate.Name,
				EntityType: candidate.EntityType,
				Score:      score,
			})
		}
	}

	// Sort by score descending
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].Score > matches[i].Score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	// Limit to maxResults
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	return matches
}
