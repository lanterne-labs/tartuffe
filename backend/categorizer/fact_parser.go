package categorizer

import (
	"digital-mirror/backend" // Need backend package for the Fact struct type
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// parseFactJSON takes the raw JSON string content returned by an LLM (expected to be a JSON array
// of objects matching the Fact structure defined in the prompt) and parses it into a slice of Fact structs.
// It handles potential wrapping in markdown code blocks (```json ... ```).
// It also performs basic validation on the parsed facts.
func parseFactJSON(jsonData []byte, sourceURL string, publishedAt *time.Time) ([]backend.Fact, error) { // Use backend.Fact type
	// Temporary struct mirroring the expected JSON output fields from the LLM prompt
	var extractedFactsRaw []struct {
		NormalizedFact string   `json:"normalized_fact"` // Match the prompt's JSON key
		OriginalQuote  string   `json:"original_quote"`  // Match the prompt's JSON key
		Topics         []string `json:"topics"`
		InvolvedPeople []string `json:"involved_people"`
	}

	jsonContent := string(jsonData)
	if err := json.Unmarshal(jsonData, &extractedFactsRaw); err != nil {
		// Sometimes the LLM might wrap the JSON in ```json ... ```, try to strip it.
		trimmedContent := strings.TrimSpace(jsonContent)
		if strings.HasPrefix(trimmedContent, "```json") {
			trimmedContent = strings.TrimPrefix(trimmedContent, "```json")
			trimmedContent = strings.TrimSuffix(trimmedContent, "```")
			trimmedContent = strings.TrimSpace(trimmedContent)
			jsonData = []byte(trimmedContent) // Use trimmed data for retry
			if retryErr := json.Unmarshal(jsonData, &extractedFactsRaw); retryErr == nil {
				err = nil // Clear the error if retry succeeded
			} else {
				// Combine errors if retry also failed
				err = fmt.Errorf("failed to decode LLM JSON content: %w (original content: %s). Retry after trimming backticks also failed: %v", err, jsonContent, retryErr)
			}
		} else {
			err = fmt.Errorf("failed to decode LLM JSON content: %w (original content: %s)", err, jsonContent)
		}
		if err != nil {
			log.Error().Err(err).Str("raw_json", jsonContent).Msg("Failed to decode LLM JSON response for facts")
			return nil, err // Return the detailed error
		}
	}

	// Convert raw extracted facts to the final Fact struct, adding context
	finalFacts := make([]backend.Fact, 0, len(extractedFactsRaw))
	for i, rawFact := range extractedFactsRaw {
		// Basic validation: Ensure normalized fact and at least one person exist
		if rawFact.NormalizedFact == "" || len(rawFact.InvolvedPeople) == 0 {
			// Log skipped facts at Warn level, ADD sourceURL
			log.Warn().Int("fact_index", i).
				Str("reason", "missing normalized_fact or involved_people").
				Str("source_url", sourceURL). // Add source URL
				Interface("raw_fact", rawFact).
				Msg("Skipping invalid fact from LLM")
			continue
		}
		finalFacts = append(finalFacts, backend.Fact{
			NormalizedFactText: rawFact.NormalizedFact, // Map to correct field
			OriginalQuote:      rawFact.OriginalQuote,  // Map to correct field
			Topics:             rawFact.Topics,
			InvolvedPeople:     rawFact.InvolvedPeople,
			SourceURL:          sourceURL, // Add context passed from extractor
			PublishedAt:        publishedAt,
		})
	}

	return finalFacts, nil
}
