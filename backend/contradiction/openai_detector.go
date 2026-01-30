package contradiction

import (
	"context"
	"digital-mirror/backend/categorizer" // Need access to baseLLMClient and potentially request/response structs
	"digital-mirror/backend/metrics"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// MaxPairsPerBatch is the maximum number of fact pairs to analyze in a single LLM call
	MaxPairsPerBatch = 5
	// DelayBetweenBatches is the delay between batch API calls to respect rate limits
	// Groq free tier: 30 RPM, 6K TPM - 2s delay = 30 req/min max
	DelayBetweenBatches = 2 * time.Second
)

// formatDateForLLM formats a time pointer for display in the LLM prompt.
// Returns "unknown date" if the time is nil.
func formatDateForLLM(t *time.Time) string {
	if t == nil {
		return "unknown date"
	}
	return t.Format("January 2, 2006")
}

const (
	contradictionPromptTpl = `Carefully compare the following two statements about a person's actions or claims. Determine if they **logically contradict** each other, paying close attention to the publication dates.

A **contradiction** means both statements CANNOT be simultaneously true regarding the core action or claim, considering the context provided (like dates, subjects, locations).

**CRITICAL - WHAT COUNTS AS A CONTRADICTION:**
A contradiction must be about the SAME SUBJECT - specifically about the person's own knowledge, beliefs, actions, or positions.

**IS a contradiction** when:
- The person's claim about THEMSELVES conflicts with their other claim about THEMSELVES
  - "I knew X" vs "I didn't know X"
  - "I did X" vs "I never did X"
- The person's claim about THEMSELVES conflicts with AUTHORITATIVE FINDINGS about THEM
  - Person says: "I didn't know about the scheme"
  - Court/investigators found: "She knew and supervised the scheme"
  - → This IS a contradiction because both statements are about HER knowledge/actions

**Is NOT a contradiction** when:
- Statement A is about the person's own belief/acknowledgment, but Statement B is about what an EXTERNAL PARTY (government, parliament, organization) did or didn't do as their own action
- Example: "I acknowledge we can be criticized" (her own acknowledgment) vs "The Parliament never told us" (Parliament's action) - these are about DIFFERENT SUBJECTS (her belief vs Parliament's behavior)
- Both statements mention similar TOPICS (e.g., "assistants", "funds") but are about DIFFERENT SUBJECTS (e.g., what Parliament did vs what she told her colleagues) - similar keywords do NOT make a contradiction
- Example: "The Parliament never advised us about assistants" (Parliament's action) vs "I deny telling MEPs they'd get assistants" (her communication to MEPs) - both mention "assistants" but are about completely different actions by different actors
- Statements are about different LEVELS of involvement - agreeing to a policy/system is NOT the same as personally executing it
- Example: "I couldn't say no to X about the mutualization system" (agreeing to a practice) vs "I had no role in hiring those assistants" (not involved in execution) - both can be true, different levels of involvement
- One statement is VAGUE/GENERAL while the other is SPECIFIC, and there is NO clear indication they refer to the same thing - BUT if context (names, dates, events) makes it clear they ARE about the same thing, it IS a contradiction
- Example: "I discovered some emails" (vague - which emails?) vs "I had no knowledge of emails between X and Y" (specific) - without clear indication these are the same emails, this is NOT a contradiction
- Counter-example: "I discovered the Schaffhauser emails" vs "I had no knowledge of the Schaffhauser emails" - clearly the same emails, this IS a contradiction
- **SUBJECTIVE AWARENESS vs OBJECTIVE FACTS are DIFFERENT STATEMENT TYPES**: "I wasn't aware of X" (subjective past state of mind) and "X didn't happen" (objective factual claim) are COMPATIBLE, not contradictory. Denying X happened does NOT imply you previously knew about X - it could be a conclusion reached after investigation. These statements reinforce each other: "I wasn't aware of any offense because there was none."
- This is ONLY contradictory if: earlier she says "I knew about X" or "I dealt with X" (demonstrating knowledge), then later claims "I wasn't aware of X" (cannot unknow)

**CRITICAL - TEMPORAL REASONING:**
When analyzing statements, you MUST consider the publication dates to determine if apparent contradictions are actually legitimate changes of knowledge or position over time:

1. **Earlier "didn't know" → Later "knew/claimed"**: If someone said they didn't know something on Date A, and later on Date B claimed to know it, this is NOT a contradiction - they could have learned in between.

2. **Earlier "claimed X" → Later "denied knowing X"**: If someone claimed knowledge of X on Date A, and later on Date B denied ever knowing X, this IS a contradiction - you cannot unknow something.

3. **Same date conflicts**: If two conflicting statements are from the same date, analyze them carefully for direct logical contradiction.

4. **Unknown dates**: If one or both dates are "unknown date", be SKEPTICAL about flagging contradictions. Only flag if the statements are completely irreconcilable regardless of temporal ordering (e.g., "I was in Paris" vs "I was in Tokyo" on the same referenced day).

**Do NOT flag a contradiction** if:
*   One statement is about the person's own position and the other is about an external party's actions or knowledge.
*   Statements describe different events, actions, or subjects (e.g., different bills, different meetings, different years).
*   Statements differ only in phrasing, granularity, or focus but convey compatible information.
*   One statement is simply more vague or general than the other.
*   Statements reflect gaining new knowledge or changing position over time (earlier statement → later update is natural progression).
*   Dates are unknown AND the statements could be reconciled by assuming an appropriate temporal ordering.

Statement A (dated %s): "%s"
Statement B (dated %s): "%s"

Answer format:
Start your response ONLY with "YES:" if they logically contradict, or "NO:" if they do not.
Immediately after "YES:" or "NO:", provide a concise (1 sentence maximum) explanation focusing on the core reason for the contradiction or lack thereof.

Example 1 (Learning Over Time - NOT Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen said she didn't know about the mutualization scheme."
Statement B (dated March 20, 2024): "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice."
Response:
NO: The later statement indicates she learned about the mutualization scheme after the earlier denial of knowledge.

Example 2 (Denial After Claim - Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen stated the EU Parliament was fully aware of the mutualization."
Statement B (dated March 20, 2024): "Marine Le Pen claimed she never knew about any mutualization scheme."
Response:
YES: She claimed knowledge on January 15, then denied having that knowledge on March 20 - you cannot unknow something.

Example 3 (Unknown Dates - Be Skeptical):
Statement A (dated unknown date): "CEO Jones supported policy X."
Statement B (dated unknown date): "CEO Jones opposed policy X."
Response:
NO: Without dates, these could represent a legitimate change of position over time.

Example 4 (Same Date Conflict - Contradictory):
Statement A (dated February 1, 2024): "Alice stated the project budget is $5 million."
Statement B (dated February 1, 2024): "Alice confirmed the project budget was approved at $7 million."
Response:
YES: The statements provide conflicting monetary values for the same project on the same date.

Example 5 (Different Events - NOT Contradictory):
Statement A (dated March 2022): "Senator Smith voted against bill H.R. 123."
Statement B (dated June 2024): "Senator Smith sponsored bill H.R. 789."
Response:
NO: The statements concern different actions on different bills at different times.

Example 6 (Person's Acknowledgment vs External Party's Action - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen acknowledged that reproaches can be made to her party regarding parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about assistants."
Response:
NO: Statement A is about her personal acknowledgment that criticism is warranted, while Statement B is about the EU Parliament's actions - these are different subjects (her belief vs Parliament's behavior).

Example 7 (Person's Claim vs Authoritative Finding About Them - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen claimed she was unaware of the fund misappropriation scheme."
Statement B (dated January 21, 2024): "Marine Le Pen ordered and supervised the fund misappropriation scheme according to court investigators."
Response:
YES: Her claim of ignorance directly contradicts the authoritative finding that she knew and supervised the scheme - both statements are about HER knowledge and actions.

Example 8 (Similar Keywords But Different Subjects - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about having assistants work with multiple deputies."
Statement B (dated January 20, 2024): "Marine Le Pen denied telling FN MEPs elected in 2014 that they would benefit from a parliamentary assistant."
Response:
NO: Despite both mentioning "assistants", these are about different subjects - Statement A is about what the Parliament did (advise/criticize), Statement B is about what she communicated to her MEPs. Different actors, different actions.

Example 9 (Policy Agreement vs Personal Execution - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said it was complicated to say no to Jean-Marie Le Pen regarding the mutualization of parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen denied having any role in the hiring of assistants for Gollnisch and Jean-Marie Le Pen."
Response:
NO: Agreeing to a system/practice (not refusing her father's request about mutualization) is different from personally executing hiring decisions. Both statements can be true - she accepted the policy but wasn't involved in specific hiring.

Example 10 (Vague Reference vs Specific Reference - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered emails she wasn't the recipient of."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the damaging emails between Schaffhauser and Saint-Just."
Response:
NO: Statement A refers to "some emails" vaguely without specifying which ones, while Statement B refers to specific emails between two named people. We cannot confirm these refer to the same emails, so no contradiction can be established.

Example 11 (Same Specific Reference - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered the emails between Schaffhauser and Saint-Just."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the emails between Schaffhauser and Saint-Just."
Response:
YES: Both statements explicitly reference the same specific emails (between Schaffhauser and Saint-Just), so "discovered" directly contradicts "had no knowledge of".

Example 12 (Subjective Awareness vs Objective Fact - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen declared she had 'no consciousness' of any potential offense in the parliamentary assistants affair."
Statement B (dated January 20, 2024): "Marine Le Pen stated the Rassemblement national committed no irregularity regarding the employment of Thierry Légier."
Response:
NO: These are different statement types - A is about her subjective past awareness, B is an objective factual claim. They reinforce each other: she wasn't aware of any offense because she believes none occurred. Denying wrongdoing does NOT imply prior knowledge of wrongdoing.

Now, analyze these specific statements:
Statement A (dated %s): "%s"
Statement B (dated %s): "%s"

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
func (d *OpenAIContradictionDetector) AreContradictory(ctx context.Context, textA string, dateA *time.Time, textB string, dateB *time.Time) (isContradictory bool, reason string, err error) {
	// Basic check: identical texts are not contradictory
	if textA == textB {
		metrics.IncrementContradictionChecks("not_found")
		return false, "Identical statements.", nil
	}

	timer := metrics.NewLLMTimer("chat", d.model)

	// Format dates for the LLM
	dateAStr := formatDateForLLM(dateA)
	dateBStr := formatDateForLLM(dateB)

	// Format the prompt with dates
	prompt := fmt.Sprintf(contradictionPromptTpl, dateAStr, textA, dateBStr, textB, dateAStr, textA, dateBStr, textB) // Repeat statements with dates for the final request part

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
		timer.ObserveDuration(err)
		metrics.IncrementContradictionChecks("error")
		return false, "", fmt.Errorf("failed to marshal Mistral contradiction request payload: %w", err)
	}

	// Call the base client (public method) which handles retries
	bodyBytes, err := d.baseClient.DoRequest(ctx, d.apiURL, d.apiKey, payloadBytes)
	if err != nil {
		timer.ObserveDuration(err)
		metrics.IncrementContradictionChecks("error")
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
		timer.ObserveDuration(fmt.Errorf("no content"))
		metrics.IncrementContradictionChecks("error")
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("Chat API returned no content for contradiction check")
		return false, "", fmt.Errorf("Chat API returned no content for contradiction check")
	}

	timer.ObserveDuration(nil)

	// --- Parse YES:/NO: response ---
	llmResponse := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	llmResponse = strings.TrimPrefix(llmResponse, "Response:") // Handle potential "Response:" prefix
	llmResponse = strings.TrimSpace(llmResponse)

	if strings.HasPrefix(llmResponse, "YES:") {
		isContradictory = true
		reason = strings.TrimSpace(strings.TrimPrefix(llmResponse, "YES:"))
		metrics.IncrementContradictionChecks("found")
		log.Warn().Str("textA", textA).Str("textB", textB).Str("reason", reason).Msg("Contradiction DETECTED")
	} else if strings.HasPrefix(llmResponse, "NO:") {
		isContradictory = false
		reason = strings.TrimSpace(strings.TrimPrefix(llmResponse, "NO:"))
		metrics.IncrementContradictionChecks("not_found")
		log.Info().Str("textA", textA).Str("textB", textB).Str("reason", reason).Msg("No contradiction detected")
	} else {
		log.Warn().Str("llm_response", llmResponse).Msg("Contradiction check LLM response did not start with YES: or NO:")
		isContradictory = false
		reason = fmt.Sprintf("Unexpected LLM response format: %s", llmResponse)
		metrics.IncrementContradictionChecks("not_found")
	}

	return isContradictory, reason, nil
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
	Error           error
}

const batchPromptTpl = `You are analyzing multiple pairs of statements for contradictions. For EACH pair, determine if the statements logically contradict each other.

**RULES FOR CONTRADICTION:**
- A contradiction means both statements CANNOT be simultaneously true
- Must be about the SAME SUBJECT (the person's own knowledge, beliefs, actions)
- Person's claim vs authoritative finding about them = CAN be contradiction
- Person's claim vs external party's action = NOT a contradiction (different subjects)
- Similar keywords but different subjects = NOT a contradiction
- Vague vs specific without clear link = NOT a contradiction
- Policy agreement vs personal execution = NOT a contradiction (different levels)
- Subjective awareness vs objective fact = NOT a contradiction ("I wasn't aware of X" + "X didn't happen" are compatible and reinforce each other - denying X happened doesn't imply prior knowledge of X)

**TEMPORAL RULES:**
- Earlier "didn't know" → Later "knew" = NOT contradiction (learned over time)
- Earlier "claimed X" → Later "denied X" = IS contradiction (cannot unknow)
- Unknown dates = be SKEPTICAL, only flag if completely irreconcilable

**PAIRS TO ANALYZE:**
%s

**RESPONSE FORMAT:**
For each pair, respond with EXACTLY this format on its own line:
PAIR N: YES: reason
or
PAIR N: NO: reason

Where N is the pair number (1, 2, 3, etc.).
Respond for ALL pairs, one per line.`

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

	prompt := fmt.Sprintf(batchPromptTpl, pairsText.String())

	// Build Chat Request
	reqPayload := categorizer.ChatRequest{
		Model: d.model,
		Messages: []categorizer.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
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
	// Initialize all results with their index
	for i := range results {
		results[i].PairIndex = i
		results[i].Reason = "No response parsed for this pair"
	}

	// Regex to match "PAIR N: YES: reason" or "PAIR N: NO: reason"
	pairRegex := regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*(YES|NO)\s*:\s*(.*)`)

	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

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
