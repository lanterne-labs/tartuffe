package util

import (
	"testing"
)

func TestRemoveAccents(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"café", "cafe"},
		{"naïve", "naive"},
		{"Björk", "Bjork"},
		{"François", "Francois"},
		{"résumé", "resume"},
		{"noaccents", "noaccents"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := RemoveAccents(tt.input)
			if got != tt.expected {
				t.Errorf("RemoveAccents(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeForFuzzy(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Marine Le Pen", "marine pen"},
		{"François de La Rochefoucauld", "francois rochefoucauld"},
		{"Jean-Luc Mélenchon", "jean-luc melenchon"},
		{"Emmanuel Macron", "emmanuel macron"},
		{"Le Parti Socialiste", "parti socialiste"},
		{"Les Républicains", "republicains"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeForFuzzy(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeForFuzzy(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		s1       string
		s2       string
		expected int
	}{
		{"", "", 0},
		{"hello", "hello", 0},
		{"hello", "hallo", 1},
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
		{"", "test", 4},
		{"test", "", 4},
	}

	for _, tt := range tests {
		t.Run(tt.s1+"_"+tt.s2, func(t *testing.T) {
			got := LevenshteinDistance(tt.s1, tt.s2)
			if got != tt.expected {
				t.Errorf("LevenshteinDistance(%q, %q) = %d, want %d", tt.s1, tt.s2, got, tt.expected)
			}
		})
	}
}

func TestSimilarityScore(t *testing.T) {
	tests := []struct {
		s1              string
		s2              string
		expectedMin     float64 // Minimum expected score
		expectedExact   bool    // Should be exact match (1.0)?
		expectedHigh    bool    // Should be high similarity (>0.8)?
		expectedMedium  bool    // Should be medium similarity (>0.5)?
	}{
		{"Marine Le Pen", "Marine Le Pen", 0.0, true, false, false},
		{"Marine Le Pen", "marine le pen", 0.0, true, false, false},
		{"Marine Le Pen", "Marine Pen", 0.0, false, true, false},
		{"François de La Rochefoucauld", "Francois Rochefoucauld", 0.0, false, true, false},
		{"Emmanuel Macron", "Emanuel Macron", 0.0, false, true, false},
		{"Jean Dupont", "John Smith", 0.0, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.s1+"_vs_"+tt.s2, func(t *testing.T) {
			score := SimilarityScore(tt.s1, tt.s2)

			if tt.expectedExact && score != 1.0 {
				t.Errorf("SimilarityScore(%q, %q) = %.3f, want 1.0 (exact match)", tt.s1, tt.s2, score)
			}

			if tt.expectedHigh && score <= 0.8 {
				t.Errorf("SimilarityScore(%q, %q) = %.3f, want > 0.8 (high similarity)", tt.s1, tt.s2, score)
			}

			if tt.expectedMedium && score <= 0.5 {
				t.Errorf("SimilarityScore(%q, %q) = %.3f, want > 0.5 (medium similarity)", tt.s1, tt.s2, score)
			}

			if score < tt.expectedMin {
				t.Errorf("SimilarityScore(%q, %q) = %.3f, want >= %.3f", tt.s1, tt.s2, score, tt.expectedMin)
			}

			t.Logf("SimilarityScore(%q, %q) = %.3f", tt.s1, tt.s2, score)
		})
	}
}

func TestFindBestMatches(t *testing.T) {
	candidates := []struct {
		ID         int64
		Name       string
		EntityType string
	}{
		{1, "Marine Le Pen", "person"},
		{2, "Emmanuel Macron", "person"},
		{3, "Jean-Luc Mélenchon", "person"},
		{4, "François Hollande", "person"},
		{5, "Rassemblement National", "party"},
		{6, "La République En Marche", "party"},
	}

	tests := []struct {
		query          string
		threshold      float64
		maxResults     int
		expectAtLeast  int
		expectTopMatch string
	}{
		{
			query:          "Marine Pen",
			threshold:      0.5,
			maxResults:     5,
			expectAtLeast:  1,
			expectTopMatch: "Marine Le Pen",
		},
		{
			query:          "Macron",
			threshold:      0.5,
			maxResults:     5,
			expectAtLeast:  1,
			expectTopMatch: "Emmanuel Macron",
		},
		{
			query:          "Melenchon",
			threshold:      0.5,
			maxResults:     5,
			expectAtLeast:  1,
			expectTopMatch: "Jean-Luc Mélenchon",
		},
		{
			query:          "RN",
			threshold:      0.3,
			maxResults:     5,
			expectAtLeast:  1,
			expectTopMatch: "Rassemblement National",
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			matches := FindBestMatches(tt.query, candidates, tt.threshold, tt.maxResults)

			if len(matches) < tt.expectAtLeast {
				t.Errorf("FindBestMatches(%q) returned %d matches, want at least %d",
					tt.query, len(matches), tt.expectAtLeast)
			}

			if len(matches) > 0 {
				topMatch := matches[0]
				if topMatch.Name != tt.expectTopMatch {
					t.Errorf("FindBestMatches(%q) top match = %q (score: %.3f), want %q",
						tt.query, topMatch.Name, topMatch.Score, tt.expectTopMatch)
				}

				t.Logf("Top match for %q: %s (score: %.3f)", tt.query, topMatch.Name, topMatch.Score)

				// Log all matches
				for i, m := range matches {
					t.Logf("  %d. %s (%.3f)", i+1, m.Name, m.Score)
				}
			}
		})
	}
}
