package contradiction

import (
	"context"
	"digital-mirror/backend/categorizer" // Need access to baseLLMClient and potentially request/response structs
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	contradictionPromptTpl = `Carefully compare the following two statements about people's actions or claims. Determine if they **logically contradict** each other.

A **contradiction** means both statements CANNOT be simultaneously true regarding the core action or claim, considering the context provided (like dates, subjects, locations).

**Do NOT flag a contradiction** if:
*   Statements describe different events, actions, or subjects (e.g., different bills, different meetings, different years unless a direct conflict is stated).
*   Statements differ only in phrasing, granularity, or focus but convey compatible information.
*   One statement is simply more vague or general than the other.
*   Statements reflect a change of opinion over a significant time period *unless* they make conflicting claims about a specific past event.

Statement A: "%s"
Statement B: "%s"

Answer format:
Start your response ONLY with "YES:" if they logically contradict, or "NO:" if they do not.
Immediately after "YES:" or "NO:", provide a concise (1 sentence maximum) explanation focusing on the core reason for the contradiction or lack thereof.

Example 1 (Different Events):
Statement A: "Senator Smith voted against bill H.R. 123 in 2022."
Statement B: "Senator Smith sponsored bill H.R. 789 in 2024."
Response:
NO: The statements concern different actions on different bills in different years.

Example 2 (Change of Stance - Not Contradictory):
Statement A: "CEO Jones opposed policy X in the January 10th meeting."
Statement B: "CEO Jones announced support for policy X on February 15th."
Response:
NO: The statements indicate a change in stance over time, not a direct contradiction about a single event.

Example 3 (Change of Stance - Contradictory):
Statement A: "CEO Jones denied ever supporting policy X during the January 10th meeting."
Statement B: "Records show CEO Jones voted in favor of policy X at the January 10th meeting."
Response:
YES: The statements make conflicting claims about CEO Jones' specific actions regarding policy X at the same meeting.

Example 4 (Conflicting Detail):
Statement A: "Alice stated the project budget is $5 million."
Statement B: "Alice confirmed the project budget was approved at $7 million."
Response:
YES: The statements provide conflicting monetary values attributed to the same person regarding the same project budget.

Example 5 (Compatible Info):
Statement A: "Bob attended the climate summit in Paris."
Statement B: "Bob was present at the Paris climate summit last week."
Response:
NO: Both statements convey compatible information about Bob's attendance.

Now, analyze these specific statements:
Statement A: "%s"
Statement B: "%s"

Response:`
)

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

	// Build chat completions URL the same way as categorizer
	base = strings.TrimRight(base, "/")
	apiURL := base + "/chat/completions"

	return &OpenAIContradictionDetector{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: baseClient,
	}, nil
}

// AreContradictory implements the ContradictionDetector interface.
func (d *OpenAIContradictionDetector) AreContradictory(ctx context.Context, textA string, textB string) (isContradictory bool, reason string, err error) {
	// Basic check: identical texts are not contradictory
	if textA == textB {
		return false, "Identical statements.", nil
	}

	// Format the prompt
	prompt := fmt.Sprintf(contradictionPromptTpl, textA, textB, textA, textB) // Repeat statements for the final request part

	// Build Chat Request using public struct types
	reqPayload := categorizer.ChatRequest{
		Model: d.model,
		Messages: []categorizer.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return false, "", fmt.Errorf("failed to marshal Mistral contradiction request payload: %w", err)
	}

	// Call the base client (public method) which handles retries
	bodyBytes, err := d.baseClient.DoRequest(ctx, d.apiURL, d.apiKey, payloadBytes)
	if err != nil {
		log.Error().Err(err).Str("textA", textA).Str("textB", textB).Msg("Contradiction check API call failed after retries")
		return false, "", fmt.Errorf("contradiction check API call failed: %w", err)
	}

	// Parse the response using public struct type
	var chatResp categorizer.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode chat API response envelope for contradiction check")
		return false, "", fmt.Errorf("failed to decode chat API response envelope: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("Chat API returned no content for contradiction check")
		return false, "", fmt.Errorf("Chat API returned no content for contradiction check")
	}

	// --- Parse YES:/NO: response ---
	llmResponse := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	llmResponse = strings.TrimPrefix(llmResponse, "Response:") // Handle potential "Response:" prefix
	llmResponse = strings.TrimSpace(llmResponse)

	if strings.HasPrefix(llmResponse, "YES:") {
		isContradictory = true
		reason = strings.TrimSpace(strings.TrimPrefix(llmResponse, "YES:"))
		log.Warn().Str("textA", textA).Str("textB", textB).Str("reason", reason).Msg("Contradiction DETECTED")
	} else if strings.HasPrefix(llmResponse, "NO:") {
		isContradictory = false
		reason = strings.TrimSpace(strings.TrimPrefix(llmResponse, "NO:"))
		log.Info().Str("textA", textA).Str("textB", textB).Str("reason", reason).Msg("No contradiction detected")
	} else {
		log.Warn().Str("llm_response", llmResponse).Msg("Contradiction check LLM response did not start with YES: or NO:")
		isContradictory = false
		reason = fmt.Sprintf("Unexpected LLM response format: %s", llmResponse)
	}

	return isContradictory, reason, nil
}
