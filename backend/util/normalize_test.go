package util

import "testing"

func TestNormalizePersonName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Éric Ciotti", "eric ciotti"},
		{"eric ciotti", "eric ciotti"},
		{"  Élisabeth  Borne ", "elisabeth  borne"}, // accents + case + trim (inner spacing preserved)
		{"François Hollande", "francois hollande"},
		{"Jean-Luc Mélenchon", "jean-luc melenchon"}, // hyphen kept, accent stripped
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizePersonName(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizePersonName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
			// Idempotent: normalizing an already-normalized name is a no-op.
			if again := NormalizePersonName(got); again != got {
				t.Errorf("NormalizePersonName not idempotent: %q -> %q", got, again)
			}
		})
	}
}

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
