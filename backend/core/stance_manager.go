package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// StanceStorer defines the storage interface for stance operations.
type StanceStorer interface {
	InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error)
	UpdateStanceDocument(ctx context.Context, id int64, summary string, embedding []float32, keyFactIDs []int64, factCount int) error
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error)
	DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error
	RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason string) error
}

// StanceDocument is the core type for stance documents.
type StanceDocument struct {
	ID               int64
	PersonID         int64
	ClusterLabel     string
	ClusterEmbedding []float32
	Summary          string
	KeyFactIDs       []int64
	FactCount        int
}

// StanceMatch is a stance found by embedding search.
type StanceMatch struct {
	StanceDocument
	Distance float64
}

// StanceFactInfo represents a fact within a stance.
type StanceFactInfo struct {
	ID                 int64
	NormalizedFactText string
	EmbeddingText      string
	CanonicalVerb      string
}

// StanceProcessResult holds the outcome of processing a new fact against existing stances.
type StanceProcessResult struct {
	Verdict            string // "NEW_STANCE", "DUPLICATE", "NEW_INFO", "CONTRADICTION"
	StanceID           int64
	Reasoning          string
	ContradictedFactID int64 // Only set for CONTRADICTION
}

// stanceLLMResponse is the expected JSON response from the stance analysis LLM call.
type stanceLLMResponse struct {
	Reasoning           string `json:"reasoning"`
	Verdict             string `json:"verdict"`
	UpdatedSummary      string `json:"updated_summary"`
	ContradictingFactID int64  `json:"contradicting_key_fact_id"`
}

// topicAssignmentResponse is the expected JSON response from the topic assignment LLM call.
type topicAssignmentResponse struct {
	Reasoning     string `json:"reasoning"`
	AssignedIndex int    `json:"assigned_topic_index"` // 1-indexed, 0 means new topic
	NewTopicLabel string `json:"new_topic_label"`      // only set when AssignedIndex == 0
}

// TopicGroupingResponse is the expected JSON response from the topic grouping LLM call (used by backfill).
type TopicGroupingResponse struct {
	Groups []TopicGroup `json:"groups"`
}

// TopicGroup represents a group of related topic labels merged into one.
type TopicGroup struct {
	GroupName string   `json:"group_name"`
	Labels    []string `json:"labels"`
}

// summaryLLMResponse is the expected JSON response from the summary generation LLM call.
type summaryLLMResponse struct {
	Summary    string  `json:"summary"`
	KeyFactIDs []int64 `json:"key_fact_ids"`
}

// StanceManager handles the core stance document logic: matching new facts to existing
// stances, creating new stances, and detecting contradictions via LLM analysis.
type StanceManager struct {
	storer    StanceStorer
	apiKey    string
	apiURL    string
	model     string
	client *llm.BaseLLMClient
}

// NewStanceManagerFromEnv creates a new StanceManager using OPEN_AI_* environment variables.
// Returns an error if required env vars (OPEN_AI_KEY, OPEN_AI_API) are missing.
func NewStanceManagerFromEnv(storer StanceStorer) (*StanceManager, error) {
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

	base = strings.TrimRight(base, "/")
	apiURL := base + "/chat/completions"

	return &StanceManager{
		storer: storer,
		apiKey: apiKey,
		apiURL: apiURL,
		model:  model,
		client: llm.NewBaseLLMClient(),
	}, nil
}

// ProcessNewFact analyzes a new fact against existing stances for the given person.
// It uses LLM-based topic assignment to find the right stance, then analyzes the relationship.
func (sm *StanceManager) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	stances, err := sm.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("getting stances for person: %w", err)
	}

	// No existing stances: skip LLM call, create new stance directly
	if len(stances) == 0 {
		label := truncate(factText, 120)
		log.Info().
			Int64("fact_id", factID).
			Str("label", label).
			Msg("No existing stances for person, creating first stance")
		return sm.createNewStance(ctx, personID, factID, factText, embedding, label)
	}

	// Ask LLM which topic this fact belongs to
	assignment, err := sm.assignTopic(ctx, stances, factText)
	if err != nil {
		return nil, fmt.Errorf("topic assignment: %w", err)
	}

	// New topic: create a new stance
	if assignment.AssignedIndex == 0 {
		label := assignment.NewTopicLabel
		log.Info().
			Str("new_topic", label).
			Int64("fact_id", factID).
			Str("reasoning", assignment.Reasoning).
			Msg("LLM assigned fact to new topic")
		return sm.createNewStance(ctx, personID, factID, factText, embedding, label)
	}

	// Find the matched stance by index (1-indexed)
	if assignment.AssignedIndex < 1 || assignment.AssignedIndex > len(stances) {
		log.Warn().
			Int("assigned_index", assignment.AssignedIndex).
			Int("total_stances", len(stances)).
			Msg("LLM returned out-of-range topic index, creating new stance")
		return sm.createNewStance(ctx, personID, factID, factText, embedding, "")
	}
	matched := &stances[assignment.AssignedIndex-1]

	log.Info().
		Int64("stance_id", matched.ID).
		Str("topic", matched.ClusterLabel).
		Int64("fact_id", factID).
		Str("reasoning", assignment.Reasoning).
		Msg("LLM assigned fact to existing topic")

	// Get key facts for the matched stance and analyze relationship
	keyFacts, err := sm.storer.GetFactsForStance(ctx, matched.ID)
	if err != nil {
		return nil, fmt.Errorf("getting facts for stance %d: %w", matched.ID, err)
	}

	llmResp, err := sm.callLLM(ctx, matched.Summary, keyFacts, factText)
	if err != nil {
		return nil, fmt.Errorf("LLM stance analysis: %w", err)
	}

	// Add the fact to the stance join table regardless of verdict
	if err := sm.storer.AddFactToStance(ctx, matched.ID, factID); err != nil {
		return nil, fmt.Errorf("adding fact to stance %d: %w", matched.ID, err)
	}

	return sm.handleVerdict(ctx, matched, personID, factID, embedding, llmResp)
}

// createNewStance creates a new stance document for a fact. If label is empty, uses the fact text.
func (sm *StanceManager) createNewStance(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, label string) (*StanceProcessResult, error) {
	if label == "" {
		label = factText
	}
	if len(label) > 120 {
		label = label[:120]
	}

	doc := StanceDocument{
		PersonID:         personID,
		ClusterLabel:     strings.TrimSpace(label),
		ClusterEmbedding: embedding,
		Summary:          factText,
		KeyFactIDs:       []int64{factID},
		FactCount:        1,
	}

	stanceID, err := sm.storer.InsertStanceDocument(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("inserting new stance document: %w", err)
	}

	if err := sm.storer.AddFactToStance(ctx, stanceID, factID); err != nil {
		return nil, fmt.Errorf("adding fact to new stance: %w", err)
	}

	log.Info().
		Int64("stance_id", stanceID).
		Int64("fact_id", factID).
		Int64("person_id", personID).
		Str("label", doc.ClusterLabel).
		Msg("Created new stance document")

	return &StanceProcessResult{
		Verdict:  "NEW_STANCE",
		StanceID: stanceID,
	}, nil
}

// handleVerdict processes the LLM analysis verdict and updates the stance accordingly.
func (sm *StanceManager) handleVerdict(ctx context.Context, matched *StanceDocument, personID int64, factID int64, embedding []float32, llmResp *stanceLLMResponse) (*StanceProcessResult, error) {
	result := &StanceProcessResult{
		StanceID:  matched.ID,
		Reasoning: llmResp.Reasoning,
	}

	switch llmResp.Verdict {
	case "DUPLICATE":
		result.Verdict = "DUPLICATE"
		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Str("reasoning", llmResp.Reasoning).
			Msg("Fact is duplicate of existing stance")

	case "NEW_INFO":
		result.Verdict = "NEW_INFO"
		newCentroid := updateCentroid(matched.ClusterEmbedding, embedding, matched.FactCount)
		newFactCount := matched.FactCount + 1

		keyFactIDs := matched.KeyFactIDs
		if len(keyFactIDs) < 10 {
			keyFactIDs = append(keyFactIDs, factID)
		}

		if err := sm.storer.UpdateStanceDocument(ctx, matched.ID, llmResp.UpdatedSummary, newCentroid, keyFactIDs, newFactCount); err != nil {
			return nil, fmt.Errorf("updating stance document %d: %w", matched.ID, err)
		}

		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("new_fact_count", newFactCount).
			Msg("Added new info to existing stance")

	case "CONTRADICTION":
		result.Verdict = "CONTRADICTION"
		result.ContradictedFactID = llmResp.ContradictingFactID

		if llmResp.ContradictingFactID > 0 {
			if err := sm.storer.RecordContradiction(ctx, personID, factID, llmResp.ContradictingFactID, llmResp.Reasoning); err != nil {
				return nil, fmt.Errorf("recording contradiction: %w", err)
			}
		} else {
			log.Warn().
				Int64("stance_id", matched.ID).
				Int64("fact_id", factID).
				Msg("LLM detected contradiction but could not identify specific contradicted fact")
		}

		newCentroid := updateCentroid(matched.ClusterEmbedding, embedding, matched.FactCount)
		newFactCount := matched.FactCount + 1

		keyFactIDs := matched.KeyFactIDs
		if len(keyFactIDs) < 10 {
			keyFactIDs = append(keyFactIDs, factID)
		}

		if err := sm.storer.UpdateStanceDocument(ctx, matched.ID, llmResp.UpdatedSummary, newCentroid, keyFactIDs, newFactCount); err != nil {
			return nil, fmt.Errorf("updating stance document %d after contradiction: %w", matched.ID, err)
		}

		log.Warn().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int64("contradicted_fact_id", llmResp.ContradictingFactID).
			Str("reasoning", llmResp.Reasoning).
			Msg("Contradiction detected in stance")

	default:
		return nil, fmt.Errorf("unexpected LLM verdict: %q", llmResp.Verdict)
	}

	return result, nil
}

// callLLM sends the stance analysis prompt to the LLM and parses the response.
func (sm *StanceManager) callLLM(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string) (*stanceLLMResponse, error) {
	// Format key facts for the prompt
	var factsBuilder strings.Builder
	for _, f := range keyFacts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`You are analyzing whether a new fact about a political figure updates, duplicates, or contradicts their known stance on a topic.

EXISTING STANCE SUMMARY:
%s

KEY FACTS IN THIS STANCE:
%s

NEW FACT:
%s

Determine the relationship between the new fact and the existing stance:

1. DUPLICATE - The new fact says essentially the same thing as the existing stance (paraphrase, same claim from different source)
2. NEW_INFO - The new fact adds new information to this topic without contradicting the stance (additional detail, update, new development)
3. CONTRADICTION - The new fact directly contradicts a specific claim in the existing stance

**CONTRADICTION RULES (apply strictly):**
- A contradiction requires the SAME SUBJECT's own statements/actions to conflict
- Promise-breaker framework: identify which fact is the "promise" and which is the potential "breaker"
- Authoritative findings (conviction, court ruling) CAN contradict a person's denial
- Accusations, charges, investigations do NOT contradict denials (procedural steps, not findings)
- Earlier "didn't know" → later "knew" is NOT a contradiction (learning over time)
- Earlier "claimed X" → later "denied X" IS a contradiction (cannot unknow)
- Downplaying ≠ contradicting (admitting imperfections doesn't contradict denying criminal intent)
- Same position in different words = DUPLICATE, not contradiction
- Complementary facts about the same event (different aspects of same ruling/sentence) = NEW_INFO, not contradiction
- Coherent defense (multiple denials about different aspects of same case) = DUPLICATE or NEW_INFO
- Legal process stages (accusation → trial → conviction) are sequential progression, not contradictions

**WORKED EXAMPLES:**

Example 1 (DUPLICATE):
Stance: "Marine Le Pen denied any wrongdoing in the parliamentary assistants case."
New fact: "Marine Le Pen rejected accusations of fund misappropriation in the assistants affair."
→ reasoning: Both express the same denial of wrongdoing in the same case, just different wording.
→ verdict: DUPLICATE

Example 2 (NEW_INFO):
Stance: "Marine Le Pen was convicted of embezzlement in the parliamentary assistants case."
New fact: "Marine Le Pen was sentenced to five years of ineligibility with immediate effect."
→ reasoning: The new fact adds sentencing details to the known conviction. These are supplementary details about the same event, not a contradiction.
→ verdict: NEW_INFO

Example 3 (CONTRADICTION):
Stance: "Marine Le Pen claimed she was fully aware of and approved the mutualization scheme."
New fact: "Marine Le Pen stated she never knew about any mutualization scheme."
→ reasoning: She previously claimed full awareness, now denies any knowledge. Per temporal rules, earlier "claimed X" followed by later "denied X" IS a contradiction — you cannot unknow something.
→ verdict: CONTRADICTION

Example 4 (NOT a contradiction — accusation vs denial):
Stance: "Marine Le Pen denied any involvement in fictitious employment."
New fact: "Marine Le Pen was charged with embezzlement in the parliamentary assistants case."
→ reasoning: Being charged is a procedural step, NOT an authoritative finding. Per contradiction rules, accusations/charges do NOT contradict denials. Only a conviction or court finding would.
→ verdict: NEW_INFO

**RESPONSE FORMAT:**
You MUST write your reasoning BEFORE committing to a verdict. Analyze the facts, check which rules apply, then decide.

Return ONLY valid JSON:
{
  "reasoning": "1-3 sentences: identify core claims, check which rules apply, then state your conclusion",
  "verdict": "DUPLICATE|NEW_INFO|CONTRADICTION",
  "updated_summary": "If NEW_INFO or CONTRADICTION: updated 2-4 sentence summary incorporating the new fact. If DUPLICATE: empty string.",
  "contradicting_key_fact_id": 0
}

CRITICAL: If your reasoning concludes the facts are compatible, supplementary, or not contradictory, your verdict MUST NOT be CONTRADICTION.
For CONTRADICTION: set contradicting_key_fact_id to the ID of the specific key fact that is contradicted. If you cannot identify one, use 0.
For DUPLICATE and NEW_INFO: set contradicting_key_fact_id to 0.`,
		stanceSummary, factsBuilder.String(), newFactText)

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact analysis assistant. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		MaxTokens:      16384,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling stance LLM request: %w", err)
	}

	log.Debug().
		Int("key_facts", len(keyFacts)).
		Str("new_fact", truncate(newFactText, 100)).
		Msg("Sending stance analysis to LLM")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM stance request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode stance chat response")
		return nil, fmt.Errorf("decoding stance chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		log.Warn().Str("raw_response", string(bodyBytes)).Msg("Stance LLM returned no content")
		return nil, fmt.Errorf("stance LLM returned no content")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	resp, err := parseStanceLLMResponse(content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse stance LLM response")
		return nil, fmt.Errorf("parsing stance LLM response: %w", err)
	}

	return resp, nil
}

// assignTopic asks the LLM to assign a new fact to one of the existing stance topics or declare it new.
func (sm *StanceManager) assignTopic(ctx context.Context, stances []StanceDocument, factText string) (*topicAssignmentResponse, error) {
	var topicsBuilder strings.Builder
	for i, s := range stances {
		topicsBuilder.WriteString(fmt.Sprintf("%d. %q (%d facts): %s\n",
			i+1, s.ClusterLabel, s.FactCount, truncate(s.Summary, 150)))
	}

	prompt := fmt.Sprintf(`You are a topic classifier for political facts. Given a new fact and a list of existing topics about a person, decide which topic the fact belongs to.

EXISTING TOPICS:
%s
NEW FACT:
%s

RULES:
- Assign the fact to an existing topic if it is about the SAME subject matter, even if it adds new details or uses different wording.
- Topics about the same legal case, event, policy area, or political issue should be consolidated.
- Example: a fact about "sentencing in the parliamentary assistants case" belongs to the same topic as "conviction in the parliamentary assistants case" — they are the same legal proceeding.
- Example: a fact about "presidential candidacy after conviction" belongs with other "presidential candidacy" facts.
- Only create a NEW topic if the fact is about a genuinely different subject that does not fit any existing topic.
- When in doubt, assign to the closest existing topic rather than creating a new one.
- Prefer broader topic matches over narrow distinctions.

LABEL RULES (for new_topic_label when creating a new topic):
- Include the SPECIFIC case, event, or policy name (e.g., "parliamentary assistants case", "2027 presidential election")
- Include a TEMPORAL ANCHOR when relevant: trial year, election cycle, date range (e.g., "2025 appeal trial", "Jan 2026 ruling")
- The label must be specific enough that a DIFFERENT event on the same broad theme would clearly NOT match
- Do NOT include the person's name (labels are already per-person)
- Bad: "justice politique" / "presidential candidacy" / "party funding"
- Good: "denial of wrongdoing -- PA case 2025 appeal" / "presidential candidacy post-ineligibility 2027" / "EU fund embezzlement allegations 2024"

Return ONLY valid JSON:
{
  "reasoning": "Brief explanation of your choice",
  "assigned_topic_index": 0,
  "new_topic_label": "specific descriptive label, max 120 chars (only if assigned_topic_index is 0)"
}

Set assigned_topic_index to the number of the matching topic (1-%d), or 0 if no existing topic fits.`, topicsBuilder.String(), factText, len(stances))

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact topic classifier. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		MaxTokens:      16384,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling topic assignment request: %w", err)
	}

	log.Debug().
		Int("existing_topics", len(stances)).
		Str("new_fact", truncate(factText, 100)).
		Msg("Sending topic assignment to LLM")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM topic assignment request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("decoding topic assignment response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return nil, fmt.Errorf("topic assignment LLM returned no content")
	}

	content := stripMarkdownCodeBlock(strings.TrimSpace(chatResp.Choices[0].Message.Content))

	var resp topicAssignmentResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse topic assignment response")
		return nil, fmt.Errorf("parsing topic assignment response: %w", err)
	}

	return &resp, nil
}

// GroupTopicLabels asks the LLM to group related topic labels into broader themes.
// Used by backfill to merge fragmented topic labels before creating stances.
func (sm *StanceManager) GroupTopicLabels(ctx context.Context, labels []string) (*TopicGroupingResponse, error) {
	if len(labels) == 0 {
		return &TopicGroupingResponse{}, nil
	}

	var labelsBuilder strings.Builder
	for i, l := range labels {
		labelsBuilder.WriteString(fmt.Sprintf("%d. %s\n", i+1, l))
	}

	prompt := fmt.Sprintf(`You are grouping topic labels about a political figure into broader themes. Many labels refer to the SAME subject matter but are worded differently.

LABELS:
%s
RULES:
- A "topic" is a SUBJECT (a specific policy area, case, event, person, or institution) — NOT a rhetorical pattern, behavioural trait, or speech act.
- NEVER group by HOW the figure communicates (announcements, denials, flip-flops, exaggerations, promises, accusations). These are forms, not topics.
- ALWAYS group by WHAT the labels are about (tariffs, NATO membership, parliamentary assistants case, immigration policy, healthcare reform).
- Bad group name: "policy announcements" / "campaign promises" / "denials and reversals" / "controversial statements" — these describe form, not subject.
- Good group name: "US tariffs on Chinese goods (2025)" / "NATO Article 5 commitment" / "embezzlement -- parliamentary assistants case (2024-2025 trial)" / "border wall construction policy".
- If two labels share a subject but disagree (one supports, one opposes; one announces, one reverses), MERGE them — that disagreement is exactly what the bundle is meant to capture.
- If two labels share a form (both are "announcements", both are "denials") but concern DIFFERENT subjects (announcement about tariffs vs. announcement about troop withdrawal), KEEP them separate.
- Example: "fund-embezzlement generalized-system", "fund-embezzlement instructions", "embezzlement European-funds", "embezzlement public-funds parliamentary-assistants-case" should ALL be in one group about embezzlement (same subject).
- Example: "ineligibility sentence democratic", "ineligibility decision contestation", "ineligibility first-instance conviction" should ALL be in one group about ineligibility (same subject).
- Example: "tariff policy China", "tariff policy steel imports", "tariff policy Mexico", "tariff policy reversal" should be split BY COUNTRY/SECTOR, NOT lumped under "tariff announcements".
- Choose a clear, specific group name (max 120 chars) with enough subject context to distinguish it from sibling topics.
- Include a TEMPORAL ANCHOR when relevant: year, trial phase, election cycle.
- Every label MUST appear in exactly one group. No label should be left out.
- Keep truly distinct subjects separate (e.g., "acquittal public-insult case" is different from "parliamentary assistants case conviction").
- Err on the side of SPLITTING when subjects differ; err on the side of MERGING only when subjects truly match.

Return ONLY valid JSON:
{
  "groups": [
    {"group_name": "descriptive name", "labels": ["label1", "label2"]},
    {"group_name": "another topic", "labels": ["label3"]}
  ]
}`, labelsBuilder.String())

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a topic grouping assistant for political facts. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		MaxTokens:      16384,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling topic grouping request: %w", err)
	}

	log.Debug().Int("label_count", len(labels)).Msg("Sending topic labels to LLM for grouping")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM topic grouping request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("decoding topic grouping response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return nil, fmt.Errorf("topic grouping LLM returned no content")
	}

	content := stripMarkdownCodeBlock(strings.TrimSpace(chatResp.Choices[0].Message.Content))

	var resp TopicGroupingResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse topic grouping response")
		return nil, fmt.Errorf("parsing topic grouping response: %w", err)
	}

	log.Info().Int("groups", len(resp.Groups)).Int("input_labels", len(labels)).Msg("LLM grouped topic labels")

	return &resp, nil
}

// stripMarkdownCodeBlock removes optional markdown code block wrappers from LLM output.
func stripMarkdownCodeBlock(content string) string {
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			content = content[:idx]
		}
		return strings.TrimSpace(content)
	}
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			content = content[:idx]
		}
		return strings.TrimSpace(content)
	}
	return content
}

// parseStanceLLMResponse parses the LLM's JSON response, handling optional markdown code blocks.
func parseStanceLLMResponse(content string) (*stanceLLMResponse, error) {
	content = stripMarkdownCodeBlock(strings.TrimSpace(content))

	var resp stanceLLMResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON in stance response: %w", err)
	}

	// Validate verdict
	switch resp.Verdict {
	case "DUPLICATE", "NEW_INFO", "CONTRADICTION":
		// valid
	default:
		return nil, fmt.Errorf("invalid verdict %q: must be DUPLICATE, NEW_INFO, or CONTRADICTION", resp.Verdict)
	}

	return &resp, nil
}

// GenerateSummary takes a list of facts and generates a summary paragraph plus up to 10 key fact IDs.
// This is intended for backfilling stance documents from existing fact clusters.
func (sm *StanceManager) GenerateSummary(ctx context.Context, facts []StanceFactInfo) (string, []int64, error) {
	if len(facts) == 0 {
		return "", nil, fmt.Errorf("no facts provided for summary generation")
	}

	// Format facts for the prompt
	var factsBuilder strings.Builder
	for _, f := range facts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`Summarize the following facts about a political figure into a concise stance paragraph (2-4 sentences). Also select up to 10 key facts that best represent this stance (by ID).

FACTS:
%s

Return ONLY valid JSON:
{
  "summary": "Concise summary paragraph...",
  "key_fact_ids": [1, 2, 3]
}`, factsBuilder.String())

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact summarization assistant. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		MaxTokens:      16384,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling summary LLM request: %w", err)
	}

	log.Debug().Int("fact_count", len(facts)).Msg("Sending facts to LLM for summary generation")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return "", nil, fmt.Errorf("LLM summary request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode summary chat response")
		return "", nil, fmt.Errorf("decoding summary chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("summary LLM returned no content")
	}

	content := stripMarkdownCodeBlock(strings.TrimSpace(chatResp.Choices[0].Message.Content))

	var resp summaryLLMResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse summary LLM response")
		return "", nil, fmt.Errorf("parsing summary LLM response: %w", err)
	}

	if resp.Summary == "" {
		return "", nil, fmt.Errorf("LLM returned empty summary")
	}

	log.Info().
		Int("fact_count", len(facts)).
		Int("key_facts_selected", len(resp.KeyFactIDs)).
		Msg("Generated stance summary")

	return resp.Summary, resp.KeyFactIDs, nil
}

// updateCentroid computes a weighted average of the old centroid and a new embedding,
// then normalizes the result to unit length.
func updateCentroid(oldCentroid []float32, newEmbedding []float32, oldCount int) []float32 {
	if len(oldCentroid) != len(newEmbedding) {
		log.Error().
			Int("old_len", len(oldCentroid)).
			Int("new_len", len(newEmbedding)).
			Msg("Centroid and embedding dimensions mismatch, returning new embedding")
		return newEmbedding
	}

	result := make([]float32, len(oldCentroid))
	divisor := float32(oldCount + 1)

	for i := range result {
		result[i] = (oldCentroid[i]*float32(oldCount) + newEmbedding[i]) / divisor
	}

	// Normalize to unit length
	var sumSq float64
	for _, v := range result {
		sumSq += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sumSq))
	if norm > 0 {
		for i := range result {
			result[i] /= norm
		}
	}

	return result
}
