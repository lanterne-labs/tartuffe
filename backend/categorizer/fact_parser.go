package categorizer

import (
	"bytes"
	"digital-mirror/backend" // Need backend package for the Fact struct type
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
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
		StatementDate      string   `json:"statement_date"`       // Optional YYYY, YYYY-MM or YYYY-MM-DD: when the statement itself was made
	}

	jsonContent := string(jsonData)

	// Pre-process: strip markdown code blocks if present
	jsonData = []byte(llm.StripJSONCodeFence(jsonContent))

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
				Str("fact", util.TruncateString(rawFact.NormalizedFact, 80)).
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
				Str("normalized_fact", util.TruncateString(rawFact.NormalizedFact, 120)).
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
				Str("normalized_fact", util.TruncateString(rawFact.NormalizedFact, 120)).
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
				Str("normalized_fact", util.TruncateString(rawFact.NormalizedFact, 120)).
				Str("source_url", sourceURL).
				Msg("Skipping fact: lang is missing (required BCP-47 code)")
			continue
		}
		// The prompt's ATTRIBUTION rule forbids crediting an aide's statement to
		// the person they work for, yet the model still does it about 1 time in 4
		// when the quote itself names the aide ("le bras droit de X a dénoncé…").
		if rawFact.SubjectEntityType == "human" && spokenByProxy(rawFact.SubjectEntity, rawFact.OriginalQuote) {
			log.Warn().Int("fact_index", i).
				Str("subject_entity", rawFact.SubjectEntity).
				Str("original_quote", util.TruncateString(rawFact.OriginalQuote, 160)).
				Str("source_url", sourceURL).
				Msg("Skipping fact: spoken by an aide or relative of the subject, not the subject")
			metrics.RecordDecision(metrics.StageFactParse, metrics.OutcomeProxySpeaker)
			continue
		}
		// Translation is no longer produced at extraction time: the prompt emits only
		// the native normalized_fact, and the async translation phase fills every
		// target language later. normalized_fact_en is thus expected to be empty here;
		// if an older prompt/model still returns it, we keep it as a harmless seed.

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

		var statementDate *time.Time
		if raw := strings.TrimSpace(rawFact.StatementDate); raw != "" {
			if d, err := backend.ParseStatementDate(raw); err == nil {
				statementDate = &d
			} else {
				log.Debug().Err(err).Int("fact_index", i).Str("source_url", sourceURL).
					Msg("LLM statement_date not a year, month or day — ignoring")
			}
		}

		finalFacts = append(finalFacts, backend.Fact{
			SubjectEntity:        rawFact.SubjectEntity,                                 // Primary entity this fact is about
			SubjectEntityType:    rawFact.SubjectEntityType,                             // Entity type (human/party/organization)
			NormalizedFactText:   isoDatesInWords(rawFact.NormalizedFact, rawFact.Lang), // Native-language fact text, dates in words
			NormalizedFactTextEn: rawFact.NormalizedFactEn,                              // English translation (empty if source is English)
			Lang:                 rawFact.Lang,                                          // BCP-47 source language
			EmbeddingText:        rawFact.EmbeddingText,                                 // Name-free version for embeddings
			OriginalQuote:        html.UnescapeString(rawFact.OriginalQuote),            // Map to correct field
			CanonicalVerb:        rawFact.CanonicalVerb,
			InvolvedPeople:       rawFact.InvolvedPeople, // Secondary people mentioned
			SourceURL:            sourceURL,              // Add context passed from extractor
			PublishedAt:          publishedAt,            // Plugin-supplied default; plugin overrides via PickPublishedAt
			ArticlePublishedAt:   llmArticleDate,         // Optional LLM signal; plugin chooses final
			StatementDate:        statementDate,          // Optional: the date of the statement itself
		})
	}

	return finalFacts, nil
}

// proxyRole matches a role that speaks on someone's behalf, followed by the
// "of" that ties it to that someone ("le bras droit de", "l'entourage d'",
// "an aide to").
const proxyRole = `(?:bras droit|proches?|entourage|conseill(?:er|ère)s?|porte-parole|collaborat(?:eur|rice)s?|right[- ]hand(?: man| woman)?|aides?|spokes(?:person|man|woman)|advis[eo]rs?)\s+(?:de\s+|d['’]\s*|of\s+|to\s+)`

// roleSubjectRe matches a subject that is a role description rather than a name.
var roleSubjectRe = regexp.MustCompile(`(?i)^(?:(?:le|la|les|l['’]|un|une|the|an?)\s*)?` + proxyRole)

// spokenByProxy reports whether the fact is really an aide's statement: the
// subject is a role ("le bras droit de X"), or the quote introduces the
// speaker as a role of the subject ("le bras droit de X a dénoncé…").
func spokenByProxy(subject, quote string) bool {
	subject = strings.TrimSpace(subject)
	if roleSubjectRe.MatchString(subject) {
		return true
	}
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}])` + proxyRole + regexp.QuoteMeta(subject)).MatchString(quote)
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

// extractFirstJSON returns the JSON value of data the model settled on,
// discarding the text around it. That is the first complete value, unless the
// text after it holds a later array of facts: the model then corrected itself
// ("Correction: the canonical_verb must be one of the allowed tokens.
// Corrected output: [...]") and the last array is its answer. Keeping the
// first one stored the answer the model withdrew: a fact with a verb outside
// the enum was dropped although the corrected array held it (2026-11-11).
func extractFirstJSON(data []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return data // let downstream error handling deal with it
	}
	rest := data[dec.InputOffset():]
	if len(bytes.TrimSpace(rest)) == 0 {
		return raw
	}
	msg := "Stripped trailing text from LLM JSON response"
	if corrected, ok := lastFactArray(rest); ok && len(raw) > 0 && raw[0] == '[' {
		raw = corrected
		msg = "LLM corrected its JSON response; keeping the last array"
	}
	log.Warn().Str("trailing_text", util.TruncateString(strings.TrimSpace(string(rest)), 400)).Msg(msg)
	return raw
}

// lastFactArray returns the last JSON array in text whose elements are all
// objects (an empty array counts: "the correct output is: []"). An array of
// anything else ("see [2]") is prose, not an answer.
func lastFactArray(text []byte) (json.RawMessage, bool) {
	var last json.RawMessage
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(text[i:]))
		var items []json.RawMessage
		if err := dec.Decode(&items); err != nil {
			continue
		}
		objects := true
		for _, it := range items {
			if t := bytes.TrimSpace(it); len(t) == 0 || t[0] != '{' {
				objects = false
				break
			}
		}
		end := i + int(dec.InputOffset())
		if objects {
			last = json.RawMessage(bytes.TrimSpace(text[i:end]))
		}
		i = end - 1
	}
	return last, last != nil
}
