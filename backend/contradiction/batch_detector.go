package contradiction

import (
	"digital-mirror/backend/llm"
	"fmt"
	"strings"
)

// PrepareContradictionBatch converts fact pairs into LLM batch requests
// suitable for submission to a Gemini batch job.
// Pairs are grouped into sub-batches of MaxPairsPerBatch.
// Each BatchRequest's CustomID encodes the pair metadata as "c:{personID}:{factA}:{factB}[,{factA}:{factB},...]".
func PrepareContradictionBatch(personID int64, pairs []FactPair, factAIDs, factBIDs []int64) []llm.BatchRequest {
	if len(pairs) == 0 {
		return nil
	}

	var requests []llm.BatchRequest

	for i := 0; i < len(pairs); i += MaxPairsPerBatch {
		end := i + MaxPairsPerBatch
		if end > len(pairs) {
			end = len(pairs)
		}
		batch := pairs[i:end]
		batchAIDs := factAIDs[i:end]
		batchBIDs := factBIDs[i:end]

		// Build the pairs section of the prompt
		var pairsText strings.Builder
		for j, pair := range batch {
			dateAStr := formatDateForLLM(pair.DateA)
			dateBStr := formatDateForLLM(pair.DateB)
			pairsText.WriteString(fmt.Sprintf("\nPAIR %d:\n", j+1))
			pairsText.WriteString(fmt.Sprintf("Statement A (dated %s): \"%s\"\n", dateAStr, pair.TextA))
			pairsText.WriteString(fmt.Sprintf("Statement B (dated %s): \"%s\"\n", dateBStr, pair.TextB))
		}

		prompt := fmt.Sprintf(contradictionPromptTpl, pairsText.String())

		// Build custom ID encoding all pair IDs
		var pairIDs []string
		for j := range batch {
			pairIDs = append(pairIDs, fmt.Sprintf("%d:%d", batchAIDs[j], batchBIDs[j]))
		}
		customID := fmt.Sprintf("c:%d:%s", personID, strings.Join(pairIDs, ","))

		requests = append(requests, llm.BatchRequest{
			CustomID: customID,
			Messages: []llm.ChatMessage{
				{Role: "user", Content: prompt},
			},
			Temperature: 0.1,
		})
	}

	return requests
}

// ParseContradictionCustomID parses a custom ID from a contradiction batch request.
// Format: "c:{personID}:{factA}:{factB}[,{factA}:{factB},...]"
// Returns personID and slice of (factAID, factBID) pairs.
func ParseContradictionCustomID(customID string) (personID int64, factAIDs, factBIDs []int64, err error) {
	if !strings.HasPrefix(customID, "c:") {
		return 0, nil, nil, fmt.Errorf("not a contradiction custom ID: %s", customID)
	}

	// Split: "c", personID, pairPart
	parts := strings.SplitN(customID, ":", 3)
	if len(parts) != 3 {
		return 0, nil, nil, fmt.Errorf("malformed contradiction custom ID: %s", customID)
	}

	if _, err := fmt.Sscanf(parts[1], "%d", &personID); err != nil {
		return 0, nil, nil, fmt.Errorf("failed to parse person ID from %s: %w", customID, err)
	}

	pairStrs := strings.Split(parts[2], ",")
	for _, ps := range pairStrs {
		var a, b int64
		if _, err := fmt.Sscanf(ps, "%d:%d", &a, &b); err != nil {
			return 0, nil, nil, fmt.Errorf("failed to parse pair IDs from %s: %w", ps, err)
		}
		factAIDs = append(factAIDs, a)
		factBIDs = append(factBIDs, b)
	}

	return personID, factAIDs, factBIDs, nil
}

// ProcessContradictionResponse parses an LLM response containing contradiction verdicts.
// Returns BatchResults in the same order as the pairs encoded in the prompt.
func ProcessContradictionResponse(response string, pairCount int) []BatchResult {
	results := make([]BatchResult, pairCount)
	parseBatchResponse(response, results)
	return results
}
