package util

import (
	"strings"
)

// ChunkText splits a large text into smaller overlapping chunks, respecting paragraph boundaries.
// It aims for chunks close to chunkSizeChars.
// NOTE: Overlap logic uses a fixed number of paragraphs (overlapParas) instead of overlapChars.
func ChunkText(text string, chunkSizeChars int, overlapChars int) []string {
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return []string{} // Return empty slice for empty or whitespace-only input
	}

	// Split by double newline and filter out empty strings immediately
	allParagraphs := strings.Split(trimmedText, "\n\n")
	paragraphs := make([]string, 0, len(allParagraphs))
	for _, p := range allParagraphs {
		p = strings.TrimSpace(p)
		if p != "" {
			paragraphs = append(paragraphs, p)
		}
	}

	if len(paragraphs) == 0 {
		return []string{}
	}

	// If the whole text (after filtering empty paras) fits, return it as one chunk
	// Rebuild the text to check length accurately based on filtered paragraphs
	rebuiltTextBuilder := strings.Builder{}
	for i, p := range paragraphs {
		if i > 0 {
			rebuiltTextBuilder.WriteString("\n\n")
		}
		rebuiltTextBuilder.WriteString(p)
	}
	rebuiltText := rebuiltTextBuilder.String()
	if len(rebuiltText) <= chunkSizeChars {
		return []string{rebuiltText}
	}

	var chunks []string
	var currentChunkBuilder strings.Builder
	currentLength := 0
	currentChunkParaStartIndex := 0 // Index in `paragraphs` where the current chunk started
	overlapParas := 2               // Fixed paragraph overlap

	for i := 0; i < len(paragraphs); i++ {
		paragraph := paragraphs[i]
		paragraphLength := len(paragraph)
		separatorLength := 0
		if currentChunkBuilder.Len() > 0 {
			separatorLength = 2 // Account for adding "\n\n"
		}

		// Check if adding the next paragraph would exceed the chunk size
		if currentLength+separatorLength+paragraphLength > chunkSizeChars && currentLength > 0 {
			// Finalize the current chunk
			chunks = append(chunks, currentChunkBuilder.String())

			// Calculate overlap start index based on fixed number of paragraphs
			overlapStartIndex := i - overlapParas

			// Ensure overlapStartIndex is not negative and not before the start index of the chunk just added.
			if overlapStartIndex < currentChunkParaStartIndex {
				overlapStartIndex = currentChunkParaStartIndex
			}
			// Ensure we make progress. If overlap would start at or before the previous chunk's start,
			// force it to start at the paragraph that caused the break.
			if overlapStartIndex <= currentChunkParaStartIndex && i > currentChunkParaStartIndex {
				overlapStartIndex = i
			}

			// Reset for the next chunk
			currentChunkBuilder.Reset()
			currentLength = 0
			currentChunkParaStartIndex = overlapStartIndex // Where the next chunk builder will start accumulating
			i = overlapStartIndex - 1                      // Set loop index back (-1 compensates for loop increment)

			continue // Continue to the next iteration to add the starting overlap paragraph
		}

		// Add separator if needed
		if separatorLength > 0 {
			currentChunkBuilder.WriteString("\n\n")
			currentLength += separatorLength
		}

		// Add the paragraph
		currentChunkBuilder.WriteString(paragraph)
		currentLength += paragraphLength
	}

	// Add the final chunk if it contains anything
	if currentChunkBuilder.Len() > 0 {
		chunks = append(chunks, currentChunkBuilder.String())
	}

	return chunks
}
