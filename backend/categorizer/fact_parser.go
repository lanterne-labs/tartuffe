package categorizer

import (
	"bytes"
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
		SubjectEntity     string   `json:"subject_entity"`      // The entity this fact is about
		SubjectEntityType string   `json:"subject_entity_type"` // "human", "party", or "organization"
		NormalizedFact    string   `json:"normalized_fact"`     // Match the prompt's JSON key
		EmbeddingText     string   `json:"embedding_text"`      // Name-free version for embedding generation
		OriginalQuote     string   `json:"original_quote"`      // Match the prompt's JSON key
		Topics            []string `json:"topics"`
		InvolvedPeople    []string `json:"involved_people"`
	}

	jsonContent := string(jsonData)

	// Pre-process: strip markdown code blocks if present
	trimmedContent := strings.TrimSpace(jsonContent)
	if strings.HasPrefix(trimmedContent, "```json") {
		trimmedContent = strings.TrimPrefix(trimmedContent, "```json")
		trimmedContent = strings.TrimSuffix(trimmedContent, "```")
		trimmedContent = strings.TrimSpace(trimmedContent)
		jsonData = []byte(trimmedContent)
	} else if strings.HasPrefix(trimmedContent, "```") {
		trimmedContent = strings.TrimPrefix(trimmedContent, "```")
		trimmedContent = strings.TrimSuffix(trimmedContent, "```")
		trimmedContent = strings.TrimSpace(trimmedContent)
		jsonData = []byte(trimmedContent)
	}

	// Handle case where LLM returns a single object instead of an array
	trimmedForCheck := strings.TrimSpace(string(jsonData))
	if strings.HasPrefix(trimmedForCheck, "{") && !strings.HasPrefix(trimmedForCheck, "[") {
		// Wrap single object in an array
		jsonData = []byte("[" + trimmedForCheck + "]")
		log.Debug().Msg("Wrapped single fact object in array")
	}

	jsonData = extractFirstJSON(jsonData)

	if err := json.Unmarshal(jsonData, &extractedFactsRaw); err != nil {
		err = fmt.Errorf("failed to decode LLM JSON content: %w (original content: %s)", err, jsonContent)
		log.Error().Err(err).Str("raw_json", jsonContent).Msg("Failed to decode LLM JSON response for facts")
		return nil, err
	}

	// Convert raw extracted facts to the final Fact struct, adding context
	finalFacts := make([]backend.Fact, 0, len(extractedFactsRaw))
	for i, rawFact := range extractedFactsRaw {
		// Basic validation: Ensure normalized fact and subject_entity exist
		if rawFact.NormalizedFact == "" || rawFact.SubjectEntity == "" {
			// Log skipped facts at Warn level, ADD sourceURL
			log.Warn().Int("fact_index", i).
				Str("reason", "missing normalized_fact or subject_entity").
				Str("source_url", sourceURL). // Add source URL
				Interface("raw_fact", rawFact).
				Msg("Skipping invalid fact from LLM")
			continue
		}

		// Warn if embedding_text is missing (required field)
		if rawFact.EmbeddingText == "" {
			log.Warn().Int("fact_index", i).
				Str("normalized_fact", rawFact.NormalizedFact).
				Str("source_url", sourceURL).
				Msg("LLM did not provide embedding_text, using normalized_fact as fallback")
			rawFact.EmbeddingText = rawFact.NormalizedFact
		}

		finalFacts = append(finalFacts, backend.Fact{
			SubjectEntity:      rawFact.SubjectEntity,     // Primary entity this fact is about
			SubjectEntityType:  rawFact.SubjectEntityType, // Entity type (human/party/organization)
			NormalizedFactText: rawFact.NormalizedFact,    // Map to correct field
			EmbeddingText:      rawFact.EmbeddingText,     // Name-free version for embeddings
			OriginalQuote:      rawFact.OriginalQuote,     // Map to correct field
			Topics:             rawFact.Topics,
			InvolvedPeople:     rawFact.InvolvedPeople, // Secondary people mentioned
			SourceURL:          sourceURL,              // Add context passed from extractor
			PublishedAt:        publishedAt,
		})
	}

	return finalFacts, nil
}

// extractFirstJSON uses json.Decoder to extract the first complete JSON value
// from data, discarding any trailing text the LLM may have appended.
func extractFirstJSON(data []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return data // let downstream error handling deal with it
	}
	// If there was trailing content, log a warning
	if dec.More() || dec.InputOffset() < int64(len(bytes.TrimSpace(data))) {
		log.Warn().Str("trailing_text", strings.TrimSpace(string(data[dec.InputOffset():]))).
			Msg("Stripped trailing text from LLM JSON response")
	}
	return raw
}
