package extractor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebcrawlerAPIExtractor_MissingAPIKey(t *testing.T) {
	// Temporarily clear the API key
	originalKey := os.Getenv("WEBCRAWLERAPI_KEY")
	os.Unsetenv("WEBCRAWLERAPI_KEY")
	defer func() {
		if originalKey != "" {
			os.Setenv("WEBCRAWLERAPI_KEY", originalKey)
		}
	}()

	extractor, err := NewWebcrawlerAPIExtractorFromEnv()
	assert.Nil(t, err, "NewWebcrawlerAPIExtractorFromEnv should not return error when key is missing")
	assert.Nil(t, extractor, "Extractor should be nil when API key is missing")
}

func TestWebcrawlerAPIExtractor_ExtractContent_Live(t *testing.T) {
	t.Skip("WebcrawlerAPI is not currently used; skipping live test")

	// Load .env file from the project root
	err := godotenv.Overload(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Logf("Could not load .env file, proceeding with environment variables: %v", err)
	}

	apiKey := os.Getenv("WEBCRAWLERAPI_KEY")
	if apiKey == "" {
		t.Skip("WEBCRAWLERAPI_KEY not set in environment or .env file, skipping live test.")
	}

	extractor, err := NewWebcrawlerAPIExtractorFromEnv()
	require.NoError(t, err, "NewWebcrawlerAPIExtractorFromEnv() should not return an error when API key is set")
	require.NotNil(t, extractor, "Extractor should not be nil when API key is set")

	// A simple, reliable URL for testing
	testURL := "https://www.example.com"

	t.Logf("Attempting to extract content from: %s using WebcrawlerAPI", testURL)
	extractedContent, err := extractor.ExtractContent(testURL)

	// Assertions
	assert.NoError(t, err, "ExtractContent should not return an error for a valid URL and API key")
	assert.NotNil(t, extractedContent, "ExtractedContent should not be nil on successful extraction")

	if extractedContent != nil {
		assert.NotEmpty(t, extractedContent.Content, "Extracted content (Markdown) should not be empty")
		assert.NotEmpty(t, extractedContent.Title, "Extracted title should not be empty")
		assert.Equal(t, testURL, extractedContent.URL, "Extracted URL should match the input URL")
		t.Logf("Successfully extracted title: %s", extractedContent.Title)
		t.Logf("Successfully extracted content (first 100 chars): %.100s...", extractedContent.Content)
	}
}

func TestStripTrailingSidebarSections(t *testing.T) {
	// Authentic fixture: BFMTV video page returns content that is entirely
	// the "## Sur le même sujet" related-videos block.
	bfmtvSidebarOnly := strings.Join([]string{
		"## Sur le même sujet",
		"",
		"[1:44](/lyon/replay-emissions/bonjour-lyon/video-x.html \"Title A\")",
		"",
		"### [Title A](/lyon/replay-emissions/bonjour-lyon/video-x.html \"Title A\")",
		"",
		"[0:44](/politique/video-y.html \"Title B\")",
		"",
		"### [Title B](/politique/video-y.html \"Title B\")",
	}, "\n")

	bodyThenSidebar := strings.Join([]string{
		"# Article title",
		"",
		"Le président a déclaré quelque chose d'important aujourd'hui devant l'Assemblée.",
		"",
		"Le débat a duré plusieurs heures et plusieurs ministres ont pris la parole.",
		"",
		"## À lire aussi",
		"",
		"### [Other article](/other.html \"Other\")",
	}, "\n")
	bodyThenSidebarExpected := strings.Join([]string{
		"# Article title",
		"",
		"Le président a déclaré quelque chose d'important aujourd'hui devant l'Assemblée.",
		"",
		"Le débat a duré plusieurs heures et plusieurs ministres ont pris la parole.",
	}, "\n")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"sidebar_only_bfmtv", bfmtvSidebarOnly, ""},
		{"body_then_a_lire_aussi", bodyThenSidebar, bodyThenSidebarExpected},
		{
			name:  "mid_body_h2_not_in_list_retained",
			input: "# Title\n\nIntro paragraph.\n\n## Contexte\n\nContext paragraph that should survive intact.\n\n## Conséquences\n\nMore body content.",
			want:  "# Title\n\nIntro paragraph.\n\n## Contexte\n\nContext paragraph that should survive intact.\n\n## Conséquences\n\nMore body content.",
		},
		{
			name:  "case_insensitive_uppercase_marker",
			input: "Some article body here.\n\n## SUR LE MÊME SUJET\n\n### [tile](/url)",
			want:  "Some article body here.",
		},
		{
			name:  "case_insensitive_accent_variant",
			input: "Body.\n\n## A lire aussi\n\nLinks.",
			want:  "Body.",
		},
		{"empty", "", ""},
		{"no_markers_passthrough", "# Just a body\n\nA paragraph.\n", "# Just a body\n\nA paragraph.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripTrailingSidebarSections(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}
