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
	// MaxPairsPerBatch is the maximum number of fact pairs to analyze in a single LLM call.
	// Set to 5 as a tradeoff between cost (~40% fewer calls than batch=3) and accuracy.
	// Monitor with --debug flag to check for cross-pair confusion.
	MaxPairsPerBatch = 5
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

// ContradictionRulesAndExamples is the shared rule body used by both the
// detector prompt and the auditor prompt. Keeping a single source of truth
// prevents the two prompts from drifting in their definition of what counts
// as a contradiction.
const ContradictionRulesAndExamples = `**CRITICAL - WHAT COUNTS AS A CONTRADICTION:**
A contradiction must be about the SAME SUBJECT - specifically about the person's own knowledge, beliefs, actions, or positions.

**PROMISE-BASED REASONING (core framework):**
For every pair, identify which fact is the "promise" (a claim, denial, or commitment by the person) and which is the potential "breaker" (a fact that could invalidate it). Either fact can be the promise — it is NOT necessarily the chronologically first one.

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
- SUBJECTIVE AWARENESS vs OBJECTIVE FACTS: "I wasn't aware of X" (subjective) and "X didn't happen" (objective) are COMPATIBLE — denying X happened doesn't imply prior knowledge of X. ONLY contradictory if the person demonstrably knew about X earlier
- DOWNPLAYING ≠ CONTRADICTING: Acknowledging an error, negligence, or that "things could have been better" does NOT contradict denying criminal intent or claiming good faith. A person can admit mistakes while genuinely believing they didn't commit a crime. "Bonne foi" (good faith) is COMPATIBLE with negligence. Only flag if the person explicitly DENIES the specific fact they elsewhere ADMITS
- CONVICTION does NOT contradict ACKNOWLEDGMENT OF IMPERFECTIONS: A court conviction does not contradict someone saying "things could have been more rigorous" or "reproaches can be made" — the conviction EXCEEDS the acknowledgment but does not logically contradict it. Only "I deny all wrongdoing" + conviction = contradiction
- COMPLEMENTARY FACTS about the same event: Two facts describing different ASPECTS of the same ruling, sentence, or event (e.g., prison term + ineligibility, first-instance sentence + appeal sentence) are SUPPLEMENTARY, not contradictory. Different court proceedings naturally produce different sentence details
- COHERENT DEFENSE STRATEGY: Multiple denials, rejections, or claims about DIFFERENT ASPECTS of the same case are a consistent defense, not contradictions. "Denied hiring instructions" + "denied gross negligence" = same defense posture. Only contradictory if the person explicitly ADMITS a specific claim in one statement and DENIES that SAME specific claim in another
- CONSISTENT IGNORANCE PATTERN: Multiple claims of not knowing about DIFFERENT aspects of the same situation REINFORCE each other — they form a consistent "I didn't know" defense. "Didn't know X" + "didn't know Y" = compatible. Only contradictory if one statement DEMONSTRATES knowledge that another statement DENIES
- SAME POSITION IN DIFFERENT WORDS: If both statements support the SAME claim or take the SAME side, they CANNOT contradict each other regardless of phrasing, language, or level of detail — paraphrases and translations are not contradictions
- EXPLORATORY POSTURE: Claiming to "seek to understand," "be in the discovery of," or "try to learn about" a situation is COMPATIBLE with specific instances of not knowing details — the exploration EXPLAINS the ignorance. "I was trying to understand" + "I didn't know X" = consistent. Only contradictory if the person simultaneously claims EXPERT KNOWLEDGE of the thing they claim to be discovering
- LEGAL PROCESS STAGES: Accusation, indictment, trial, and conviction are sequential stages of the SAME legal process — they do NOT contradict each other. Being accused AND later convicted is normal progression, not a contradiction. Only the person's OWN contradictory statements count
- CONVICTION SCOPE: A general conviction (e.g., "found guilty of embezzlement") does NOT automatically contradict every specific factual claim the person made about individual details within the case. Only flag if the conviction SPECIFICALLY disproves the particular claim being paired with it
- ANNOUNCEMENT FOLLOWED BY ACTION: Announcing, promising, or planning to do something and then DOING it is the OPPOSITE of a contradiction — it is a FULFILLED commitment. "X announced they would support Y" + "X came to support Y" = consistent. Only contradictory if the person announced one thing and then did the OPPOSITE
- GEOGRAPHIC/JURISDICTIONAL SCOPE: A fact about a specific region, département, or city does NOT contradict a fact about national-level data. "Only 3 communes in Vienne" does not contradict "425 lists nationwide." Always check if facts refer to different geographic scopes before flagging

**TEMPORAL REASONING:**
1. Earlier "didn't know" → Later "knew/claimed" = NOT contradiction (learned over time)
2. Earlier "claimed X" → Later "denied X" = IS contradiction (cannot unknow)
3. Same date conflicts: analyze carefully for direct logical contradiction
4. Unknown dates: be SKEPTICAL, only flag if completely irreconcilable regardless of temporal ordering

**EXAMPLES:**

PAIR 1 (Learning Over Time - NOT Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen said she didn't know about the mutualization scheme."
Statement B (dated March 20, 2024): "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice."
PAIR 1: REASONING: Statement B is dated later than A. She denied knowledge in January, then discussed mutualization as normal in March. This fits the LEARNING OVER TIME temporal rule — earlier "didn't know" followed by later knowledge is not contradictory.
PAIR 1: NO: The later statement indicates she learned about the mutualization scheme after the earlier denial of knowledge.

PAIR 2 (Denial After Claim - Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen stated she was fully aware of the mutualization scheme and that it was standard practice."
Statement B (dated March 20, 2024): "Marine Le Pen claimed she never knew about any mutualization scheme."
PAIR 2: REASONING: She claimed full awareness in January, then denied any knowledge in March. The temporal rule says earlier "claimed X" followed by later "denied X" IS a contradiction — you cannot unknow something.
PAIR 2: YES: She claimed personal knowledge of the mutualization on January 15, then denied having that knowledge on March 20 - you cannot unknow something.

PAIR 3 (Unknown Dates - Be Skeptical):
Statement A (dated unknown date): "CEO Jones supported policy X."
Statement B (dated unknown date): "CEO Jones opposed policy X."
PAIR 3: REASONING: Both dates are unknown. The statements appear opposite, but per the UNKNOWN DATES temporal rule, we must be skeptical — without dates, these could represent a legitimate change of position over time.
PAIR 3: NO: Without dates, these could represent a legitimate change of position over time.

PAIR 4 (Same Date Conflict - Contradictory):
Statement A (dated February 1, 2024): "Alice stated the project budget is $5 million."
Statement B (dated February 1, 2024): "Alice confirmed the project budget was approved at $7 million."
PAIR 4: YES: The statements provide conflicting monetary values for the same project on the same date.

PAIR 5 (Different Events - NOT Contradictory):
Statement A (dated March 2022): "Senator Smith voted against bill H.R. 123."
Statement B (dated June 2024): "Senator Smith sponsored bill H.R. 789."
PAIR 5: NO: The statements concern different actions on different bills at different times.

PAIR 6 (Person's Acknowledgment vs External Party's Action - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen acknowledged that reproaches can be made to her party regarding parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about assistants."
PAIR 6: NO: Statement A is about her personal acknowledgment that criticism is warranted, while Statement B is about the EU Parliament's actions - these are different subjects (her belief vs Parliament's behavior).

PAIR 7 (Person's Claim vs Authoritative Finding About Them - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen claimed she was unaware of the fund misappropriation scheme."
Statement B (dated January 21, 2024): "Marine Le Pen ordered and supervised the fund misappropriation scheme according to court investigators."
PAIR 7: YES: Her claim of ignorance directly contradicts the authoritative finding that she knew and supervised the scheme - both statements are about HER knowledge and actions.

PAIR 8 (Trial/Accusation vs Denial - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
PAIR 8: NO: The promise is her denial of wrongdoing. Being tried (a procedural step) is NOT a finding of guilt — it does not break her denial. Only a conviction or court finding would.

PAIR 9 (Court Finding vs Denial - Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
PAIR 9: YES: The promise is her denial of any misappropriation. The court's guilty verdict is an authoritative finding that directly breaks her denial.

PAIR 10 (Vague Reference vs Specific Reference - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered emails she wasn't the recipient of."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the damaging emails between Schaffhauser and Saint-Just."
PAIR 10: NO: Statement A refers to "some emails" vaguely without specifying which ones, while Statement B refers to specific emails between two named people. We cannot confirm these refer to the same emails, so no contradiction can be established.

PAIR 11 (Same Specific Reference - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered the emails between Schaffhauser and Saint-Just."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the emails between Schaffhauser and Saint-Just."
PAIR 11: YES: Both statements explicitly reference the same specific emails (between Schaffhauser and Saint-Just), so "discovered" directly contradicts "had no knowledge of".

PAIR 12 (Subjective Awareness vs Objective Fact - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen declared she had 'no consciousness' of any potential offense in the parliamentary assistants affair."
Statement B (dated January 20, 2024): "Marine Le Pen stated the Rassemblement national committed no irregularity regarding the employment of Thierry Légier."
PAIR 12: NO: These are different statement types - A is about her subjective past awareness, B is an objective factual claim. They reinforce each other: she wasn't aware of any offense because she believes none occurred. Denying wrongdoing does NOT imply prior knowledge of wrongdoing.

PAIR 13 (Admitting Error ≠ Admitting Crime - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit' lors de son procès."
Statement B (dated January 20, 2024): "Marine Le Pen a admis que les assistants parlementaires ont travaillé au siège du parti, ce qu'elle considère comme une erreur."
PAIR 13: NO: Admitting an error (administrative mistake) does NOT contradict denying criminal intent — a person can acknowledge mistakes while genuinely believing they committed no crime.

PAIR 14 (Good Faith + Negligence Are Compatible - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a déclaré avoir agi de 'bonne foi' lors de son procès en appel."
Statement B (dated January 22, 2024): "Marine Le Pen a concédé une certaine 'négligence' lors de son interrogatoire devant la cour d'appel."
PAIR 14: NO: Good faith and negligence are compatible — a person can be negligent while acting in good faith (e.g., trusting subordinates without adequate oversight). Only "I was careful and diligent" vs "I was negligent" would be contradictory.

PAIR 15 (Conviction vs Acknowledgment of Imperfections - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a reconnu que 'beaucoup de choses auraient pu se passer de manière plus rigoureuse' dans le traitement des assistants parlementaires."
Statement B (dated March 28, 2024): "Marine Le Pen a été condamnée pour détournement de fonds publics dans l'affaire des assistants parlementaires."
PAIR 15: NO: The conviction does not contradict her acknowledgment of imperfections — it exceeds it. She admitted things could have been better; the court found it criminal. These are different DEGREES of the same assessment, not logical opposites. Only an explicit denial of wrongdoing + conviction = contradiction.

PAIR 16 (Coherent Defense — Different Denials About Same Case - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a nié avoir donné des instructions d'embauche fictive dans l'affaire des assistants parlementaires."
Statement B (dated January 22, 2024): "Marine Le Pen a rejeté les accusations de faute lourde dans la gestion des fonds européens."
PAIR 16: NO: Both statements are denials about different aspects of the same case — denying hiring instructions and denying gross negligence form a consistent defense posture, not a contradiction.

PAIR 17 (Consistent Ignorance — Didn't Know Different Things - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a déclaré ignorer que 80%% du budget des assistants parlementaires était reversé au parti."
Statement B (dated January 22, 2024): "Marine Le Pen a affirmé ne pas savoir que Julien Odoul avait été embauché comme assistant parlementaire fictif."
PAIR 17: NO: Not knowing about the 80%% budget figure and not knowing about a specific hiring are claims of ignorance about DIFFERENT aspects — they reinforce the consistent "I didn't know" defense rather than contradicting each other.

PAIR 18 (Same Position in Different Words - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a affirmé que le RN n'a jamais dissimulé les détails de l'emploi de ses assistants parlementaires."
Statement B (dated January 22, 2024): "Marine Le Pen a déclaré que le Rassemblement national n'avait rien caché concernant les pratiques d'emploi de ses assistants."
PAIR 18: NO: Both statements express the same position — that the party did not hide information about assistant employment — using different words. Paraphrases of the same claim cannot contradict each other.

PAIR 19 (Unrelated Facts About Same Person - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a été interrogée pendant 13 heures lors de son procès en appel."
Statement B (dated March 28, 2024): "Marine Le Pen a déclaré vouloir épuiser toutes les voies de recours après sa condamnation."
PAIR 19: NO: Being interrogated for 13 hours and wanting to exhaust appeals are completely unrelated facts about different events at different times — no logical relationship exists between them.

PAIR 20 (Exploratory Posture + Specific Ignorance - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen claimed she was only seeking to understand the situation regarding the parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen admitted she did not know that her advisor was actually the parliamentary assistant of another MEP."
PAIR 20: NO: Seeking to understand and not knowing a specific detail are compatible — the ignorance explains why she was seeking to understand, not the opposite.

PAIR 21 (Legal Process Stages - NOT Contradictory):
Statement A (dated January 20, 2024): "Le Rassemblement National est accusé de détournement de fonds publics dans l'affaire des assistants parlementaires."
Statement B (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics dans l'affaire des assistants parlementaires."
PAIR 21: NO: Accusation and conviction are sequential stages of the same legal process — being accused and then convicted is normal progression, not a contradiction.

PAIR 22 (Different Descriptions of Same Sentence - NOT Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen a été condamnée à quatre ans de prison, 100 000 euros d'amende et cinq ans d'inéligibilité."
Statement B (dated March 28, 2024): "Marine Le Pen a été condamnée à cinq ans d'inéligibilité avec exécution immédiate."
PAIR 22: NO: Both describe the same first-instance sentence — Statement A gives the full breakdown while Statement B focuses on the ineligibility component. These are supplementary details, not contradictions.

PAIR 23 (Conviction vs Specific Detail Defense - NOT Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen was sentenced to five years of ineligibility for misappropriation of public funds."
Statement B (dated January 20, 2024): "Marine Le Pen denied that her parliamentary assistant was solely a bodyguard, stating he had broader administrative functions."
PAIR 23: NO: The conviction for the broad embezzlement scheme does not specifically disprove her claim about one assistant's job functions — these operate at different levels of specificity.

PAIR 24 (Coherent Defense — Different Denials Reinforcing Same Position - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen stated she was not aware of her sister's salary as a parliamentary assistant."
Statement B (dated January 22, 2024): "Marine Le Pen rejected responsibility for the salaries paid to relatives in the parliamentary assistants case."
PAIR 24: NO: Not knowing a specific salary and rejecting responsibility for relatives' salaries are both part of the same defense — distancing herself from relatives' employment terms. Two denials about the same topic reinforce each other.

PAIR 26 (Geographic/Jurisdictional Scope - NOT Contradictory):
Statement A (dated March 10, 2024): "Seulement 3 communes en Vienne ont déposé des listes pour les élections municipales."
Statement B (dated March 10, 2024): "425 listes ont été déposées sur l'ensemble du territoire national pour les élections municipales."
PAIR 26: REASONING: Statement A is about a specific département (Vienne) while Statement B is about the entire national territory. Different geographic scopes — a small local number is fully compatible with a large national total.
PAIR 26: NO: Facts refer to different geographic scopes (one département vs nationwide) — local data does not contradict national aggregates.

PAIR 25 (Announcement Followed by Action - NOT Contradictory):
Statement A (dated March 15, 2024): "Jean-Luc Mélenchon announced he would come to support his candidate in Lille on Thursday evening."
Statement B (dated March 18, 2024): "Jean-Luc Mélenchon came to support the candidacy of Lahouaria Addouche in Lille."
PAIR 25: REASONING: Statement A is an announcement of intent to support, Statement B describes the actual support happening. This is a fulfilled commitment — the person did what they announced. Past vs future tense about the same action is NOT a contradiction.
PAIR 25: NO: Announcing support and then providing that support is a fulfilled commitment, not a contradiction.`

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
		MaxTokens:   16384,
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
