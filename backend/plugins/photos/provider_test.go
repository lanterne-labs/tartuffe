package photos

import (
	"testing"

	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
)

func TestBuildSearchQuery(t *testing.T) {
	human := "human"
	party := "party"

	tests := []struct {
		name     string
		person   storage.PersonSummary
		expected string
	}{
		{
			name:     "human person",
			person:   storage.PersonSummary{Name: "Marine Le Pen", EntityType: &human},
			expected: `"Marine Le Pen" official portrait photo`,
		},
		{
			name:     "party",
			person:   storage.PersonSummary{Name: "Renaissance", EntityType: &party},
			expected: `"Renaissance" political party logo`,
		},
		{
			name:     "nil entity type defaults to human",
			person:   storage.PersonSummary{Name: "Jean Dupont"},
			expected: `"Jean Dupont" official portrait photo`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSearchQuery(tt.person)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestScoreImages(t *testing.T) {
	images := []search.ImageResult{
		{Original: "http://a.com/large.jpg", OriginalWidth: 800, OriginalHeight: 800, Source: "wikipedia.org", Position: 1},
		{Original: "http://b.com/small.jpg", OriginalWidth: 100, OriginalHeight: 100, Source: "random.com", Position: 2},
		{Original: "http://c.com/wide.jpg", OriginalWidth: 1200, OriginalHeight: 300, Source: "reuters.com", Position: 3},
		{Original: "http://d.com/medium.jpg", OriginalWidth: 500, OriginalHeight: 600, Source: "lemonde.fr", Position: 4},
	}

	scored := scoreImages(images, 200, 200)

	// Small image should be filtered out
	assert.Len(t, scored, 3)

	// First result should be the wikipedia one (high domain + good aspect ratio + high resolution)
	assert.Equal(t, "http://a.com/large.jpg", scored[0].URL)
	assert.Greater(t, scored[0].Score, 60.0) // Should be a high score

	// The wide image should score lower due to bad aspect ratio
	var wideScore float64
	for _, s := range scored {
		if s.URL == "http://c.com/wide.jpg" {
			wideScore = s.Score
		}
	}
	assert.Less(t, wideScore, scored[0].Score)
}

func TestComputeScore(t *testing.T) {
	// Perfect image: 1000x1000, wikipedia, position 0
	img := search.ImageResult{
		OriginalWidth:  1000,
		OriginalHeight: 1000,
		Source:         "wikipedia.org",
		Position:       0,
	}

	score, breakdown := computeScore(img)
	assert.Greater(t, score, 90.0) // Near-perfect score
	assert.Contains(t, breakdown, "res=")
	assert.Contains(t, breakdown, "aspect=")
	assert.Contains(t, breakdown, "domain=")
	assert.Contains(t, breakdown, "pos=")
}

func TestScoreDomain(t *testing.T) {
	assert.Equal(t, 25.0, scoreDomain("wikipedia.org"))
	assert.Equal(t, 25.0, scoreDomain("commons.wikimedia.org"))
	assert.Equal(t, 20.0, scoreDomain("reuters.com"))
	assert.Equal(t, 20.0, scoreDomain("lemonde.fr"))
	assert.Equal(t, 10.0, scoreDomain("twitter.com"))
	assert.Equal(t, 12.0, scoreDomain("unknownsite.com"))
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Marine Le Pen", "marine_le_pen"},
		{"Jean-Luc Mélenchon", "jean_luc_melenchon"},
		{"François Hollande", "francois_hollande"},
		{"Simple", "simple"},
		{"  spaces  ", "spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeName(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtensionFromContentType(t *testing.T) {
	assert.Equal(t, ".jpg", extensionFromContentType("image/jpeg"))
	assert.Equal(t, ".png", extensionFromContentType("image/png"))
	assert.Equal(t, ".gif", extensionFromContentType("image/gif"))
	assert.Equal(t, ".webp", extensionFromContentType("image/webp"))
	assert.Equal(t, ".svg", extensionFromContentType("image/svg+xml"))
	assert.Equal(t, ".jpg", extensionFromContentType("image/unknown"))
}
