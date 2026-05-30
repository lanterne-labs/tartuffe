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
		Reasoning          string   `json:"reasoning"`           // LLM reasoning (logged, not stored)
		SubjectEntity      string   `json:"subject_entity"`      // The entity this fact is about
		SubjectEntityType  string   `json:"subject_entity_type"` // "human", "party", or "organization"
		NormalizedFact     string   `json:"normalized_fact"`     // Native language fact text
		NormalizedFactEn   string   `json:"normalized_fact_en"`  // English translation (omitted when source is English)
		Lang               string   `json:"lang"`                // BCP-47 source language (required)
		CanonicalVerb      string   `json:"canonical_verb"`      // One of 20 canonical verbs
		EmbeddingText      string   `json:"embedding_text"`      // Verb-free topic for embedding generation
		OriginalQuote      string   `json:"original_quote"`      // Match the prompt's JSON key
		InvolvedPeople     []string `json:"involved_people"`
		ArticlePublishedAt string   `json:"article_published_at"` // Optional YYYY-MM-DD when LLM found a pub date in the body
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

		// Validate canonical_verb is one of the 20 known verbs.
		// Unknown verbs corrupt downstream contradiction matching and stance
		// clustering, so we drop the fact entirely (the prompt declares the
		// closed list as MUST). Empty verb falls back to the legacy
		// embedding_text prefix path before failing.
		if rawFact.CanonicalVerb == "" {
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
		if rawFact.CanonicalVerb == "" || !isKnownVerb(rawFact.CanonicalVerb) {
			log.Warn().Int("fact_index", i).
				Str("canonical_verb", rawFact.CanonicalVerb).
				Str("normalized_fact", truncateStr(rawFact.NormalizedFact, 120)).
				Str("source_url", sourceURL).
				Msg("Skipping fact: canonical_verb is empty or outside the closed enum")
			continue
		}
		// Normalise to upper-case so downstream comparisons are stable.
		rawFact.CanonicalVerb = strings.ToUpper(rawFact.CanonicalVerb)

		// Validate subject_entity_type. The prompt declares a closed enum of
		// three lowercase strings; unknown values are normalised to "human"
		// with a WARN — lower stakes than canonical_verb (which drives
		// contradiction matching), so we don't drop the whole fact.
		if rawFact.SubjectEntityType == "" {
			rawFact.SubjectEntityType = "human"
		} else if !isKnownEntityType(rawFact.SubjectEntityType) {
			log.Warn().Int("fact_index", i).
				Str("raw_entity_type", rawFact.SubjectEntityType).
				Str("normalized_fact", truncateStr(rawFact.NormalizedFact, 120)).
				Str("source_url", sourceURL).
				Msg("LLM returned unknown subject_entity_type; normalising to 'human'")
			rawFact.SubjectEntityType = "human"
		} else {
			rawFact.SubjectEntityType = strings.ToLower(strings.TrimSpace(rawFact.SubjectEntityType))
		}

		// Validate lang is set. The prompt declares it REQUIRED; missing lang would
		// produce facts with NULL language and break stance-language routing.
		rawFact.Lang = strings.ToLower(strings.TrimSpace(rawFact.Lang))
		if rawFact.Lang == "" {
			log.Warn().Int("fact_index", i).
				Str("normalized_fact", truncateStr(rawFact.NormalizedFact, 120)).
				Str("source_url", sourceURL).
				Msg("Skipping fact: lang is missing (required BCP-47 code)")
			continue
		}
		// If lang is English, normalized_fact_en should be empty (per prompt).
		// If lang is non-English, normalized_fact_en is required for the bilingual flow.
		if rawFact.Lang != "en" && strings.TrimSpace(rawFact.NormalizedFactEn) == "" {
			log.Warn().Int("fact_index", i).
				Str("lang", rawFact.Lang).
				Str("normalized_fact", truncateStr(rawFact.NormalizedFact, 120)).
				Str("source_url", sourceURL).
				Msg("non-English fact missing normalized_fact_en — storing without English copy; frontend toggle will fall back to native")
		}

		// Parse the optional LLM-extracted article publication date. Accept the
		// progressively-precise ISO 8601 forms the prompt asks for: date only,
		// date+time without tz, or full RFC3339. Anything else is silently
		// ignored so a malformed value doesn't poison the fact.
		var llmArticleDate *time.Time
		if raw := strings.TrimSpace(rawFact.ArticlePublishedAt); raw != "" {
			if d, ok := parseFlexibleISO(raw); ok {
				llmArticleDate = &d
			} else {
				log.Debug().
					Int("fact_index", i).
					Str("article_published_at", raw).
					Str("source_url", sourceURL).
					Msg("LLM article_published_at not a recognised ISO form — ignoring")
			}
		}

		finalFacts = append(finalFacts, backend.Fact{
			SubjectEntity:        rawFact.SubjectEntity,     // Primary entity this fact is about
			SubjectEntityType:    rawFact.SubjectEntityType, // Entity type (human/party/organization)
			NormalizedFactText:   rawFact.NormalizedFact,    // Native-language fact text
			NormalizedFactTextEn: rawFact.NormalizedFactEn,  // English translation (empty if source is English)
			Lang:                 rawFact.Lang,              // BCP-47 source language
			EmbeddingText:        rawFact.EmbeddingText,     // Name-free version for embeddings
			OriginalQuote:        rawFact.OriginalQuote,     // Map to correct field
			CanonicalVerb:        rawFact.CanonicalVerb,
			InvolvedPeople:       rawFact.InvolvedPeople, // Secondary people mentioned
			SourceURL:            sourceURL,              // Add context passed from extractor
			PublishedAt:          publishedAt,            // Plugin-supplied default; plugin overrides via PickPublishedAt
			ArticlePublishedAt:   llmArticleDate,         // Optional LLM signal; plugin chooses final
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

// knownEntityTypes is the closed set of accepted subject_entity_type values.
var knownEntityTypes = map[string]bool{
	"human": true, "party": true, "organization": true,
}

// isKnownEntityType checks if a subject_entity_type matches the closed enum
// declared in the extraction prompt.
func isKnownEntityType(t string) bool {
	return knownEntityTypes[strings.ToLower(strings.TrimSpace(t))]
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

// parseFlexibleISO accepts the date-or-datetime ISO 8601 forms the LLM is asked
// to emit for `article_published_at`: pure date, date+time without timezone,
// or RFC3339. Returns (parsed, true) on the first match.
func parseFlexibleISO(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
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
