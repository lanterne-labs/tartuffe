package contradiction

import (
	"context"
	"digital-mirror/backend/categorizer" // Need access to baseLLMClient and potentially request/response structs
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// MaxPairsPerBatch is the maximum number of fact pairs to analyze in a single LLM call.
	// Sized so the prompt + the contradictionMaxTokens reservation stays well
	// under a 16k context window: ContradictionRulesAndExamples + 3 pairs ~=
	// 2.6k tokens, plus contradictionMaxTokens=2048 = ~4.7k total reservation.
	// 3 pairs is the historical accuracy baseline (we briefly raised to 5 for
	// cost; cross-pair confusion observed at 5, reverted).
	MaxPairsPerBatch = 3
	// contradictionMaxTokens caps the LLM response budget. A 3-pair response
	// is ~600 tokens realistically (REASONING + verdict per pair, ~200 tok
	// each). 2048 leaves headroom while keeping prompt+reservation under 5k.
	contradictionMaxTokens = 2048
	// DelayBetweenBatches is the delay between batch API calls to respect rate limits.
	// 1s is conservative for Gemini Flash via OpenRouter (high RPM limits).
	DelayBetweenBatches = 1 * time.Second
)

// pairRegex matches "PAIR N: YES: reason" or "PAIR N: NO: reason" in LLM batch responses.
var pairRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*(YES|NO)\s*:\s*(.*)`)

// reasoningRegex matches "PAIR N: REASONING: ..." lines in LLM batch responses.
var reasoningRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*REASONING\s*:\s*(.*)`)

// formatDateForLLM formats a time pointer for display in the LLM prompt.
// Returns "unknown date" if the time is nil.
func formatDateForLLM(t *time.Time) string {
	if t == nil {
		return "unknown date"
	}
	return t.Format("January 2, 2006")
}

// ContradictionRulesAndExamples re-exports promptrules.ContradictionRules so
// the detector and auditor (and the per-stance analyzer in `core`) all share
// the same rule body without an import cycle. Edit the shared text in
// backend/promptrules/contradiction.go, not here.
const ContradictionRulesAndExamples = promptrules.ContradictionRules


const contradictionPromptTpl = `You are analyzing pairs of statements for contradictions. For EACH pair, determine if the statements logically contradict each other.

` + ContradictionRulesAndExamples + `

**PAIRS TO ANALYZE:**
%s

**RESPONSE FORMAT:**
For each pair, respond with EXACTLY two lines:

Line 1 (mandatory): PAIR N: REASONING: [Identify core claims in each statement, check which rules apply, 1-2 sentences of analysis]
Line 2 (mandatory): PAIR N: [YES/NO]: [one-sentence reason]

You MUST write your REASONING line BEFORE your verdict line. Analyze before committing.
If your reasoning concludes the statements are compatible, supplementary, or not contradictory, your verdict MUST be NO.
CRITICAL: Never output YES when your own analysis shows the facts don't contradict.`

// OpenAIContradictionDetector implements ContradictionDetector using an OpenAI-compatible API.
type OpenAIContradictionDetector struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *categorizer.BaseLLMClient // Use public type
}

// NewOpenAIContradictionDetectorFromEnv creates a detector using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API
// Optional: OPEN_AI_CHAT_MODEL (defaults to categorizer default)
func NewOpenAIContradictionDetectorFromEnv() (*OpenAIContradictionDetector, error) {
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	baseClient := categorizer.NewBaseLLMClient() // Use public constructor

	// Build chat completions URL - check if already includes the path
	base = strings.TrimRight(base, "/")
	apiURL := base
	if !strings.HasSuffix(base, "/chat/completions") {
		apiURL = base + "/chat/completions"
	}

	return &OpenAIContradictionDetector{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: baseClient,
	}, nil
}

// AreContradictory implements the ContradictionDetector interface.
// Delegates to AreContradictoryBatch with a single pair.
func (d *OpenAIContradictionDetector) AreContradictory(ctx context.Context, textA string, dateA *time.Time, textB string, dateB *time.Time) (isContradictory bool, reason string, err error) {
	// Basic check: identical texts are not contradictory
	if textA == textB {
		metrics.IncrementContradictionChecks("not_found")
		return false, "Identical statements.", nil
	}

	results := d.AreContradictoryBatch(ctx, []FactPair{{
		TextA: textA,
		DateA: dateA,
		TextB: textB,
		DateB: dateB,
	}})

	if len(results) == 0 {
		return false, "", fmt.Errorf("batch returned no results")
	}
	r := results[0]
	if r.Error != nil {
		return false, "", r.Error
	}
	return r.IsContradictory, r.Reason, nil
}

// FactPair represents a pair of facts to check for contradiction
type FactPair struct {
	IndexA int
	TextA  string
	DateA  *time.Time
	IndexB int
	TextB  string
	DateB  *time.Time
}

// BatchResult represents the result of checking a single pair in a batch
type BatchResult struct {
	PairIndex       int // Index in the input slice
	IsContradictory bool
	Reason          string
	Reasoning       string // Parsed REASONING line from LLM (analysis before verdict)
	RawResponse     string // Full LLM response text for inspection
	Error           error
}

// AreContradictoryBatch checks multiple pairs for contradictions in a single LLM call.
// Returns results for each pair in the same order as input.
func (d *OpenAIContradictionDetector) AreContradictoryBatch(ctx context.Context, pairs []FactPair) []BatchResult {
	results := make([]BatchResult, len(pairs))

	if len(pairs) == 0 {
		return results
	}

	timer := metrics.NewLLMTimer("chat", d.model)

	// Build the pairs section of the prompt
	var pairsText strings.Builder
	for i, pair := range pairs {
		dateAStr := formatDateForLLM(pair.DateA)
		dateBStr := formatDateForLLM(pair.DateB)
		pairsText.WriteString(fmt.Sprintf("\nPAIR %d:\n", i+1))
		pairsText.WriteString(fmt.Sprintf("Statement A (dated %s): \"%s\"\n", dateAStr, pair.TextA))
		pairsText.WriteString(fmt.Sprintf("Statement B (dated %s): \"%s\"\n", dateBStr, pair.TextB))
	}

	prompt := fmt.Sprintf(contradictionPromptTpl, pairsText.String())

	// Build Chat Request
	reqPayload := categorizer.ChatRequest{
		Model: d.model,
		Messages: []categorizer.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   contradictionMaxTokens,
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		timer.ObserveDuration(err)
		for i := range results {
			results[i].PairIndex = i
			results[i].Error = fmt.Errorf("failed to marshal batch request: %w", err)
			metrics.IncrementContradictionChecks("error")
		}
		return results
	}

	// Call the API
	bodyBytes, err := d.baseClient.DoRequest(ctx, d.apiURL, d.apiKey, payloadBytes)
	if err != nil {
		timer.ObserveDuration(err)
		log.Error().Err(err).Int("pairs", len(pairs)).Msg("Batch contradiction check API call failed")
		for i := range results {
			results[i].PairIndex = i
			results[i].Error = fmt.Errorf("batch API call failed: %w", err)
			metrics.IncrementContradictionChecks("error")
		}
		return results
	}

	// Parse the response
	var chatResp categorizer.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode batch response")
		for i := range results {
			results[i].PairIndex = i
			results[i].Error = fmt.Errorf("failed to decode batch response: %w", err)
		}
		return results
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		timer.ObserveDuration(fmt.Errorf("no content"))
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("Batch API returned no content")
		for i := range results {
			results[i].PairIndex = i
			results[i].Error = fmt.Errorf("batch API returned no content")
			metrics.IncrementContradictionChecks("error")
		}
		return results
	}

	timer.ObserveDuration(nil)

	// Parse each PAIR N: YES/NO: reason line
	llmResponse := chatResp.Choices[0].Message.Content

	// Store raw response on every result for inspection
	for i := range results {
		results[i].RawResponse = llmResponse
	}

	parseBatchResponse(llmResponse, results)

	// Log results and update metrics
	contradictions := 0
	for _, r := range results {
		if r.IsContradictory {
			contradictions++
			metrics.IncrementContradictionChecks("found")
		} else if r.Error == nil {
			metrics.IncrementContradictionChecks("not_found")
		} else {
			metrics.IncrementContradictionChecks("error")
		}
	}
	log.Info().Int("pairs", len(pairs)).Int("contradictions", contradictions).Msg("Batch contradiction check completed")

	return results
}

// parseBatchResponse parses the LLM response and fills in the results slice
func parseBatchResponse(response string, results []BatchResult) {
	// Initialize all results with their index — unparsed pairs are treated as errors
	for i := range results {
		results[i].PairIndex = i
		results[i].Reason = "No response parsed for this pair"
		results[i].Error = fmt.Errorf("no response parsed for pair %d", i+1)
	}

	// Match REASONING and YES/NO verdict lines

	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try REASONING line first
		if rm := reasoningRegex.FindStringSubmatch(line); rm != nil {
			var pairNum int
			fmt.Sscanf(rm[1], "%d", &pairNum)
			pairIdx := pairNum - 1
			if pairIdx >= 0 && pairIdx < len(results) {
				results[pairIdx].Reasoning = strings.TrimSpace(rm[2])
			}
			continue
		}

		// Try verdict line (YES/NO)
		matches := pairRegex.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		// Parse pair number (1-indexed in response, 0-indexed in results)
		var pairNum int
		fmt.Sscanf(matches[1], "%d", &pairNum)
		pairIdx := pairNum - 1

		if pairIdx < 0 || pairIdx >= len(results) {
			log.Warn().Int("pair_num", pairNum).Msg("Batch response contains invalid pair number")
			continue
		}

		verdict := strings.ToUpper(matches[2])
		reason := strings.TrimSpace(matches[3])

		results[pairIdx].IsContradictory = (verdict == "YES")
		results[pairIdx].Reason = reason
		results[pairIdx].Error = nil

		if results[pairIdx].IsContradictory {
			log.Warn().Int("pair", pairNum).Str("reason", reason).Msg("Batch: Contradiction detected")
		} else {
			log.Debug().Int("pair", pairNum).Str("reason", reason).Msg("Batch: No contradiction")
		}
	}
}
