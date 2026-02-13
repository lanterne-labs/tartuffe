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

**PROMISE-BASED REASONING (core framework):**
For every pair, identify which fact is the **"promise"** (a claim, denial, or commitment by the person) and which is the potential **"breaker"** (a fact that could invalidate it). Either fact can be the promise — it is NOT necessarily the chronologically first one.

A breaker genuinely invalidates a promise when:
- The person's OWN later or earlier statement directly contradicts their promise (e.g., "I knew X" vs "I never knew X")
- An AUTHORITATIVE FINDING (conviction, court ruling, official investigation conclusion) contradicts the person's denial or claim
- The person's OWN ACTION contradicts their stated commitment or position

A breaker does NOT invalidate a promise when:
- An ACCUSATION, CHARGE, INDICTMENT, or TRIAL (procedural steps) is paired with a denial — being accused or tried is NOT a finding of guilt
- An INVESTIGATION (without conclusion) is paired with a denial — being investigated does NOT establish facts
- An EXTERNAL PARTY'S action is the alleged breaker — only the person's own statements/actions or authoritative findings about them count

**Is NOT a contradiction** when:
- Statement A is about the person's own belief/acknowledgment, but Statement B is about what an EXTERNAL PARTY did or didn't do as their own action (different subjects)
- Both statements mention similar TOPICS but are about DIFFERENT SUBJECTS (e.g., what Parliament did vs what she told her colleagues) — similar keywords do NOT make a contradiction
- Statements are about different LEVELS of involvement — agreeing to a policy/system is NOT the same as personally executing it
- One statement is VAGUE/GENERAL while the other is SPECIFIC, and there is NO clear indication they refer to the same thing — BUT if context (names, dates, events) makes it clear they ARE about the same thing, it IS a contradiction
- **SUBJECTIVE AWARENESS vs OBJECTIVE FACTS**: "I wasn't aware of X" (subjective) and "X didn't happen" (objective) are COMPATIBLE — denying X happened doesn't imply prior knowledge of X. ONLY contradictory if the person demonstrably knew about X earlier

**CRITICAL - TEMPORAL REASONING:**
When analyzing statements, you MUST consider the publication dates to determine if apparent contradictions are actually legitimate changes of knowledge or position over time:

1. **Earlier "didn't know" → Later "knew/claimed"**: If someone said they didn't know something on Date A, and later on Date B claimed to know it, this is NOT a contradiction - they could have learned in between.

2. **Earlier "claimed X" → Later "denied knowing X"**: If someone claimed knowledge of X on Date A, and later on Date B denied ever knowing X, this IS a contradiction - you cannot unknow something.

3. **Same date conflicts**: If two conflicting statements are from the same date, analyze them carefully for direct logical contradiction.

4. **Unknown dates**: If one or both dates are "unknown date", be SKEPTICAL about flagging contradictions. Only flag if the statements are completely irreconcilable regardless of temporal ordering (e.g., "I was in Paris" vs "I was in Tokyo" on the same referenced day).

Statement A (dated %s): "%s"
Statement B (dated %s): "%s"

Answer format:
Start your response ONLY with "YES:" if they logically contradict, or "NO:" if they do not.
Immediately after "YES:" or "NO:", provide a concise (1 sentence maximum) explanation that identifies the promise and whether the other fact breaks it.

Example 1 (Learning Over Time - NOT Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen said she didn't know about the mutualization scheme."
Statement B (dated March 20, 2024): "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice."
Response:
NO: The later statement indicates she learned about the mutualization scheme after the earlier denial of knowledge.

Example 2 (Denial After Claim - Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen stated she was fully aware of the mutualization scheme and that it was standard practice."
Statement B (dated March 20, 2024): "Marine Le Pen claimed she never knew about any mutualization scheme."
Response:
YES: She claimed personal knowledge of the mutualization on January 15, then denied having that knowledge on March 20 - you cannot unknow something.

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

Example 8 (Trial/Accusation vs Denial - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
Response:
NO: The promise is her denial of wrongdoing. Being tried (a procedural step) is NOT a finding of guilt — it does not break her denial. Only a conviction or court finding would.

Example 9 (Court Finding vs Denial - Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
Response:
YES: The promise is her denial of any misappropriation. The court's guilty verdict is an authoritative finding that directly breaks her denial.

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
- Subjective awareness vs objective fact = NOT a contradiction ("I wasn't aware of X" + "X didn't happen" are compatible - denying X happened doesn't imply prior knowledge of X)

**PROMISE-BASED REASONING:**
- Identify which fact is the "promise" (claim/denial/commitment) and which is the potential "breaker"
- The promise can be EITHER fact (not necessarily chronologically first)
- ACCUSATIONS/CHARGES/TRIALS are procedural steps, NOT findings — do NOT break a denial
- Only CONVICTIONS/COURT FINDINGS or the person's OWN contradictory actions break a promise

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
