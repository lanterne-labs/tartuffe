package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Classification primitives shared with the OpenAI batch-API classification flow
// in batch_job.go. The synchronous per-fact classifier was removed when stance
// processing became the single ingest path; only the batch-side helpers remain
// because the batch flow still ships classification requests through OpenAI's
// batch endpoint and parses the responses here.

// ClassificationInput represents a similar fact to classify against the new fact.
type ClassificationInput struct {
	ExistingFactID   int64
	ExistingFactText string
	CanonicalVerb    string
	PublishedAt      *time.Time
}

// ClassificationResult represents the classification of one existing fact relative to the new fact.
type ClassificationResult struct {
	ExistingFactID int64
	Relation       string // "duplicate", "implication_new_is_general", "implication_new_is_specific", "unrelated"
	Reason         string
}

// llmClassificationResponse is the expected JSON response from the LLM.
type llmClassificationResponse struct {
	Classifications []llmClassification `json:"classifications"`
}

type llmClassification struct {
	ExistingFactID flexInt64 `json:"existing_fact_id"`
	Relation       string    `json:"relation"`
	Reason         string    `json:"reason"`
}

// flexInt64 unmarshals both JSON numbers and JSON strings into int64.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexInt64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("existing_fact_id must be a number or numeric string, got: %s", string(data))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("existing_fact_id string not a valid integer: %q", s)
	}
	*f = flexInt64(n)
	return nil
}

const classificationPrompt = `You are analyzing how a NEW fact relates to EXISTING facts about the same person/entity.

Each fact has a canonical_verb (the stance/action: DENIED, CLAIMED, VOTED_FOR, etc.) and text describing the topic.

For each existing fact, classify its relationship to the new fact as one of:
- "duplicate": The new fact and existing fact express the same information (possibly with different wording). Same stance on the same topic from different articles = duplicate.
- "implication_new_is_general": The new fact is MORE GENERAL - the existing fact is a specific instance or subset of the new fact.
- "implication_new_is_specific": The new fact is MORE SPECIFIC - it's a specific instance of the existing fact.
- "unrelated": The facts are about different topics or have no logical subsumption relationship.

**IMPORTANT RULES:**
- "duplicate" means they say essentially the same thing, not just that they're related
- Two articles reporting the same claim/vote/denial = duplicate (e.g., "Le Pen DENIED corruption charges" from Le Monde and "Le Pen DENIED corruption charges" from Le Figaro)
- Same topic but different stance = unrelated (e.g., "X VOTED_FOR pension reform" vs "X CRITICIZED pension reform")
- An implication means one logically ENTAILS the other (knowing A guarantees B)
- When in doubt, classify as "unrelated" - false positives are worse than misses

NEW FACT:
Verb: %s
Text: %s
Published: %s

EXISTING FACTS:
%s

Respond ONLY with a JSON object in this exact format:
{"classifications": [{"existing_fact_id": <id>, "relation": "<type>", "reason": "<brief explanation>"}]}
`

// ParseClassificationResponse parses an LLM JSON response into ClassificationResults.
// Filters out "unrelated" and invalid relation types.
func ParseClassificationResponse(jsonContent string) ([]ClassificationResult, error) {
	var classResp llmClassificationResponse
	if err := json.Unmarshal([]byte(jsonContent), &classResp); err != nil {
		return nil, fmt.Errorf("failed to parse classification JSON: %w", err)
	}

	validRelations := map[string]bool{
		"duplicate": true, "implication_new_is_general": true,
		"implication_new_is_specific": true, "unrelated": true,
	}

	var results []ClassificationResult
	for _, c := range classResp.Classifications {
		if !validRelations[c.Relation] {
			continue
		}
		if c.Relation == "unrelated" {
			continue
		}
		results = append(results, ClassificationResult{
			ExistingFactID: int64(c.ExistingFactID),
			Relation:       c.Relation,
			Reason:         c.Reason,
		})
	}
	return results, nil
}
