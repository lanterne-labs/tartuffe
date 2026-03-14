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

// ParseFactJSON takes the raw JSON string content returned by an LLM (expected to be a JSON array
// of objects matching the Fact structure defined in the prompt) and parses it into a slice of Fact structs.
// It handles potential wrapping in markdown code blocks (```json ... ```).
// It also performs basic validation on the parsed facts.
func ParseFactJSON(jsonData []byte, sourceURL string, publishedAt *time.Time) ([]backend.Fact, error) {
	// Temporary struct mirroring the expected JSON output fields from the LLM prompt
	var extractedFactsRaw []struct {
		Reasoning         string   `json:"reasoning"`           // LLM reasoning (logged, not stored)
		SubjectEntity     string   `json:"subject_entity"`      // The entity this fact is about
		SubjectEntityType string   `json:"subject_entity_type"` // "human", "party", or "organization"
		NormalizedFact    string   `json:"normalized_fact"`     // Match the prompt's JSON key
		CanonicalVerb     string   `json:"canonical_verb"`      // One of 20 canonical verbs
		EmbeddingText     string   `json:"embedding_text"`      // Verb-free topic for embedding generation
		OriginalQuote     string   `json:"original_quote"`      // Match the prompt's JSON key
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

		// Log reasoning at debug level
		if rawFact.Reasoning != "" {
			log.Debug().
				Str("reasoning", rawFact.Reasoning).
				Str("fact", truncateStr(rawFact.NormalizedFact, 80)).
				Msg("LLM extraction reasoning")
		}

		// Warn if embedding_text is missing (required field)
		if rawFact.EmbeddingText == "" {
			log.Warn().Int("fact_index", i).
				Str("normalized_fact", rawFact.NormalizedFact).
				Str("source_url", sourceURL).
				Msg("LLM did not provide embedding_text, using normalized_fact as fallback")
			rawFact.EmbeddingText = rawFact.NormalizedFact
		}

		// Validate canonical_verb is one of the 20 known verbs
		if rawFact.CanonicalVerb != "" {
			if !isKnownVerb(rawFact.CanonicalVerb) {
				log.Warn().Int("fact_index", i).
					Str("canonical_verb", rawFact.CanonicalVerb).
					Str("source_url", sourceURL).
					Msg("LLM returned unknown canonical_verb, keeping as-is")
			}
		} else {
			// Fallback: try to parse verb from embedding_text first word (backward compat)
			verb, topic, ok := ParseVerbFromEmbeddingText(rawFact.EmbeddingText)
			if ok {
				rawFact.CanonicalVerb = verb
				rawFact.EmbeddingText = topic
				log.Debug().Int("fact_index", i).
					Str("parsed_verb", verb).
					Str("topic", topic).
					Msg("Parsed canonical_verb from embedding_text (backward compat)")
			}
		}

		finalFacts = append(finalFacts, backend.Fact{
			SubjectEntity:      rawFact.SubjectEntity,     // Primary entity this fact is about
			SubjectEntityType:  rawFact.SubjectEntityType, // Entity type (human/party/organization)
			NormalizedFactText: rawFact.NormalizedFact,    // Map to correct field
			EmbeddingText:      rawFact.EmbeddingText,     // Name-free version for embeddings
			OriginalQuote:      rawFact.OriginalQuote,     // Map to correct field
			CanonicalVerb:      rawFact.CanonicalVerb,
			InvolvedPeople:     rawFact.InvolvedPeople, // Secondary people mentioned
			SourceURL:          sourceURL,              // Add context passed from extractor
			PublishedAt:        publishedAt,
		})
	}

	return finalFacts, nil
}

// knownVerbs is the set of 20 canonical verbs used in fact extraction.
var knownVerbs = map[string]bool{
	"DENIED": true, "CLAIMED": true, "PROMISED": true, "SUPPORTED": true,
	"OPPOSED": true, "VOTED_FOR": true, "VOTED_AGAINST": true, "PROPOSED": true,
	"CONFIRMED": true, "ADMITTED": true, "REFUSED": true, "DEMANDED": true,
	"THREATENED": true, "WAS_SENTENCED": true, "WAS_CONVICTED": true,
	"WAS_ACQUITTED": true, "WAS_FINED": true, "RECEIVED_FUNDS": true,
	"CHANGED_POSITION": true, "ANNOUNCED": true,
}

// isKnownVerb checks if a verb is one of the 20 canonical verbs.
func isKnownVerb(verb string) bool {
	return knownVerbs[strings.ToUpper(verb)]
}

// directClaimVerbs are verbs where the person is the speaker/actor (not a third-party attribution).
var directClaimVerbs = map[string]bool{
	"DENIED": true, "CLAIMED": true, "PROMISED": true, "SUPPORTED": true,
	"OPPOSED": true, "VOTED_FOR": true, "VOTED_AGAINST": true, "PROPOSED": true,
	"CONFIRMED": true, "ADMITTED": true, "REFUSED": true, "DEMANDED": true,
	"THREATENED": true, "ANNOUNCED": true, "CHANGED_POSITION": true,
}

// IsDirectClaimVerb returns true if the verb represents a direct claim or action
// by the person (as opposed to an external attribution like a court ruling).
// Returns false for empty strings, unknown verbs, and external verbs
// (WAS_SENTENCED, WAS_CONVICTED, WAS_ACQUITTED, WAS_FINED, RECEIVED_FUNDS).
func IsDirectClaimVerb(verb string) bool {
	return directClaimVerbs[strings.ToUpper(verb)]
}

// ParseVerbFromEmbeddingText attempts to extract a canonical verb prefix from an embedding_text string.
// Returns (verb, remainingTopic, true) if a known verb is found as the first word.
// Returns ("", "", false) if no known verb is found.
func ParseVerbFromEmbeddingText(embeddingText string) (verb string, topic string, ok bool) {
	embeddingText = strings.TrimSpace(embeddingText)
	if embeddingText == "" {
		return "", "", false
	}
	// Try single-word verbs first, then two-word verbs (VOTED_FOR, WAS_SENTENCED, etc.)
	parts := strings.SplitN(embeddingText, " ", 2)
	firstWord := strings.ToUpper(parts[0])
	if knownVerbs[firstWord] {
		remainder := ""
		if len(parts) > 1 {
			remainder = strings.TrimSpace(parts[1])
		}
		return firstWord, remainder, true
	}
	return "", "", false
}

// truncateStr truncates a string to maxLen characters.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
