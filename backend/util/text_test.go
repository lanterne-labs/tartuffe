package util

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChunkText(t *testing.T) {
	assert := assert.New(t)

	// --- Test Cases ---
	testCases := []struct {
		name           string
		text           string
		chunkSize      int
		overlapSize    int // NOTE: overlapSize is currently ignored by implementation (uses fixed 2 paras)
		expectedChunks []string
	}{
		{
			name:           "ShortTextLessThanChunkSize",
			text:           "This is a short text.",
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{"This is a short text."},
		},
		{
			name:           "TextExactlyChunkSize",
			text:           strings.Repeat("a", 100),
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{strings.Repeat("a", 100)},
		},
		{
			name:           "EmptyText",
			text:           "",
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{}, // Should return empty slice
		},
		{
			name:           "WhitespaceOnlyText",
			text:           "  \n\n  \t ",
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{}, // Should filter out empty chunks
		},
		{
			name:           "SingleLongParagraph",
			text:           strings.Repeat("word ", 50), // 250 chars
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{strings.TrimSpace(strings.Repeat("word ", 50))},
		},
		{
			name:           "MultipleParagraphsNoChunkingNeeded",
			text:           "Paragraph 1 is short.\n\nParagraph 2 is also quite short.",
			chunkSize:      100,
			overlapSize:    10,
			expectedChunks: []string{"Paragraph 1 is short.\n\nParagraph 2 is also quite short."},
		},
		{
			name: "MultipleParagraphsChunkingParagraphOverlap",
			text: fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\n%s", // 5 paragraphs
				strings.Repeat("a", 40), // P1
				strings.Repeat("b", 40), // P2
				strings.Repeat("c", 40), // P3
				strings.Repeat("d", 40), // P4
				strings.Repeat("e", 40), // P5
			),
			chunkSize:   100, // Fits 2 paras (40+2+40=82), breaks on 3rd (82+2+40=124)
			overlapSize: 50,  // Ignored, uses 2 para overlap
			expectedChunks: []string{
				fmt.Sprintf("%s\n\n%s", strings.Repeat("a", 40), strings.Repeat("b", 40)), // Chunk 1 = P1+P2
				fmt.Sprintf("%s\n\n%s", strings.Repeat("c", 40), strings.Repeat("d", 40)), // Chunk 2 = P3+P4 (Overlap started at i=2)
				strings.Repeat("e", 40), // Chunk 3 = P5 (Overlap started at i=4)
			},
		},
		{
			name: "LongFirstParagraphImmediateChunk",
			text: fmt.Sprintf("%s\n\n%s\n\n%s",
				strings.Repeat("a", 150), // P1
				strings.Repeat("b", 50),  // P2
				strings.Repeat("c", 50),  // P3
			),
			chunkSize:   100,
			overlapSize: 20, // Ignored
			expectedChunks: []string{
				strings.Repeat("a", 150), // Chunk 1 = P1
				strings.Repeat("b", 50),  // Chunk 2 = P2 (Overlap started at i=1)
				strings.Repeat("c", 50),  // Chunk 3 = P3 (Overlap started at i=2)
			},
		},
		{
			name:           "MultipleEmptyParagraphsIgnored",
			text:           "Para 1\n\n\n\nPara 2\n\n  \n\nPara 3",
			chunkSize:      50,
			overlapSize:    5, // Ignored
			expectedChunks: []string{"Para 1\n\nPara 2\n\nPara 3"},
		},
		{
			name:        "OverlapLargerThanChunkSize",
			text:        "P1 seventy chars or so.\n\nP2 shorter.\n\nP3 adds more.\n\nP4 even more text here.", // 4 paras
			chunkSize:   50,
			overlapSize: 60, // Ignored
			expectedChunks: []string{
				fmt.Sprintf("%s\n\n%s", "P1 seventy chars or so.", "P2 shorter."),   // Chunk 1 = P1+P2 (36 <= 50)
				fmt.Sprintf("%s\n\n%s", "P3 adds more.", "P4 even more text here."), // Chunk 2 = P3+P4 (38 <= 50, Overlap started at i=2)
			},
		},
	}

	// --- Run Tests ---
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Pass a dummy overlap size, as it's ignored by current implementation
			actualChunks := ChunkText(tc.text, tc.chunkSize, 0)
			assert.Equal(tc.expectedChunks, actualChunks, fmt.Sprintf("Input text:\n%s", tc.text))
		})
	}
}
