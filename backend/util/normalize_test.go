package util

import "testing"

func TestNormalizeToDirName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Marine Le Pen", "marine_le_pen"},
		{"Boris Vallaud", "boris_vallaud"},
		{"Éric Ciotti", "eric_ciotti"},
		{"Jean-Luc Mélenchon", "jean_luc_melenchon"},
		{"François Hollande", "francois_hollande"},
		{"Simple", "simple"},
		{"Multiple   Spaces", "multiple_spaces"},
		{"  Leading/Trailing  ", "leading_trailing"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := NormalizeToDirName(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeToDirName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
