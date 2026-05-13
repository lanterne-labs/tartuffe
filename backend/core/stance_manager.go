package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
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
// NewTopicLabel uses flexibleString because some LLMs (notably gemma-4) occasionally
// emit a number (e.g. 0) for this field when AssignedIndex != 0 (i.e. when no new
// topic is being created). A strict string parse would fail the whole call.
type topicAssignmentResponse struct {
	Reasoning     string         `json:"reasoning"`
	AssignedIndex int            `json:"assigned_topic_index"` // 1-indexed, 0 means new topic
	NewTopicLabel flexibleString `json:"new_topic_label"`      // only set when AssignedIndex == 0
}

// flexibleString accepts string, number, bool, or null in JSON and stores the
// canonical string form. Used for LLM-emitted fields where the model occasionally
// returns a non-string scalar despite the schema asking for a string.
type flexibleString string

func (s *flexibleString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*s = ""
		return nil
	}
	if data[0] == '"' {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*s = flexibleString(v)
		return nil
	}
	// number / bool / object / array — store the raw JSON text, trimmed.
	*s = flexibleString(strings.TrimSpace(string(data)))
	return nil
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
		label := string(assignment.NewTopicLabel)
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
	if r := []rune(label); len(r) > 120 {
		label = string(r[:120])
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

KEY FACTS IN THIS STANCE (each line is "[ID N] (verb) text"):
%s

NEW FACT:
%s

Decide ONE of:
1. DUPLICATE — the new fact says essentially the same thing as something already in the stance (paraphrase / same claim, different source).
2. NEW_INFO — the new fact adds new information to this topic without contradicting the stance.
3. CONTRADICTION — the new fact contradicts a specific KEY FACT (or the summary) per the rules below. Compare the NEW FACT against EACH key fact, not just the summary — the summary may have smoothed earlier tensions.

`+promptrules.ContradictionRules+`

**HOW TO MAP THE PAIR-WISE EXAMPLES TO THIS TASK:**
The examples above are framed as "PAIR N: YES/NO". Treat YES as your CONTRADICTION verdict, NO as your DUPLICATE or NEW_INFO verdict (DUPLICATE if the two statements are paraphrases of the SAME position; NEW_INFO if they're compatible-but-distinct).

**RESPONSE FORMAT:**
Write your reasoning BEFORE the verdict. If your reasoning concludes the facts are compatible, supplementary, or paraphrastic, your verdict MUST NOT be CONTRADICTION.

Return ONLY valid JSON:
{
  "reasoning": "1-3 sentences: identify core claims, check which positive/negative pattern applies, then state your conclusion",
  "verdict": "DUPLICATE|NEW_INFO|CONTRADICTION",
  "updated_summary": "If NEW_INFO or CONTRADICTION: updated 2-4 sentence summary that PRESERVES the tension when there is one (do not smooth a contradiction into a unified narrative). If DUPLICATE: empty string.",
  "contradicting_key_fact_id": 0
}

For CONTRADICTION: set contradicting_key_fact_id to the ID of the specific key fact that is contradicted (use the [ID N] from the KEY FACTS list). If you cannot identify one, use 0.
For DUPLICATE and NEW_INFO: set contradicting_key_fact_id to 0.`,
		stanceSummary, factsBuilder.String(), newFactText)

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact analysis assistant. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		MaxTokens:      4000,
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

⚠️ NEVER use a META / PATTERN-OF-BEHAVIOR / RHETORICAL label. The label must name a SUBJECT (a specific case, policy area, event, person, institution), NOT a way of speaking or a behavioural trait. If the only thing the fact has in common with future facts is "they are all promises he later reversed" or "they are all accusations", that is a FORM, not a topic — go down to the actual SUBJECT instead.
- ❌ "policy announcements contradictions" / "campaign promises" / "denials and reversals" / "controversial statements" / "rhetoric and threats" — these describe HOW he speaks, not WHAT about
- ❌ "flip-flops on foreign policy" / "pattern of exaggeration" — meta-summaries
- ✅ "US tariffs on Chinese goods 2025" / "NATO Article 5 commitment 2026" / "Strait of Hormuz coalition" / "Iran nuclear deal negotiations 2026"
- If the fact is itself a journalist's META observation about the entity (e.g. "X has repeatedly contradicted himself"), the upstream extractor should have rejected it; mark it as NEW_STANCE only if a specific subject is identifiable, otherwise pick the closest existing subject-bound topic.

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
		MaxTokens:      4000,
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

// StanceForMerge is the minimal view of a candidate stance the LLM needs to
// decide whether merges are warranted within an in-Go-clustered group.
type StanceForMerge struct {
	ID      int64
	Label   string
	Summary string
}

// MergeDecision is the LLM's decision for a candidate cluster: zero or more
// merge subgroups, each with a unified summary/label and the IDs of stances to
// fold together. Stances not appearing in any subgroup stay as-is.
type MergeDecision struct {
	Merges []MergeSubgroup `json:"merges"`
}

// MergeSubgroup is one subset of stances within a candidate cluster that the
// LLM has declared a single stance. StanceIDs lists the IDs to merge (must be
// ≥ 2); Summary and Label are the unified text/label for the resulting stance.
type MergeSubgroup struct {
	StanceIDs []int64 `json:"stance_ids"`
	Summary   string  `json:"summary"`
	Label     string  `json:"label"`
}

// MergeClusterDecision asks the LLM whether the supplied candidate cluster of
// stances should be merged (in whole or in subsets), and, for each merge it
// proposes, returns a unified summary + label. The cluster has already been
// produced by Phase-A average-linkage clustering, so all stances here are
// embedding-similar; the LLM's job is to filter false positives (different
// temporal context, polarity, scope) and write the new unified text.
//
// The caller is responsible for picking the destination stance (largest
// fact_count) and applying the merge through the storer.
func (sm *StanceManager) MergeClusterDecision(ctx context.Context, personName string, cluster []StanceForMerge) (*MergeDecision, error) {
	if len(cluster) < 2 {
		return &MergeDecision{}, nil
	}

	var stancesBuilder strings.Builder
	for i, s := range cluster {
		stancesBuilder.WriteString(fmt.Sprintf("[%d] id=%d, label=%q, summary=%q\n",
			i+1, s.ID, s.Label, truncate(s.Summary, 400)))
	}

	prompt := fmt.Sprintf(`You are deciding whether several stance documents about %s represent the SAME stance and should be merged, or are genuinely distinct stances.

RULES:
- Merge ONLY if all stances are about the exact same claim/topic by the same person.
- Do NOT merge stances that differ in temporal context (e.g. "in 2024" vs "in 2025"), geographic context, or polarity (supporting vs opposing).
- Do NOT merge stances that describe different events even if they share a broad theme (e.g. two separate legal cases, two separate elections).
- It is fine to leave the cluster as-is if you're unsure — false negatives are cheap, false positives are expensive.
- Singletons (single-stance "merges") are NOT allowed — every merge group must contain at least 2 stance IDs.

For each subset you decide to merge, return a UNIFIED summary (1-3 sentences) and a topic label (4-12 words) that covers all merged stances. The label must name a SUBJECT (specific case, policy, event), not a behaviour or rhetorical pattern.

STANCES IN THIS CANDIDATE CLUSTER:
%s

Return ONLY valid JSON:
{
  "reasoning": "1-3 sentences explaining your decision",
  "merges": [
    {"stance_ids": [12, 34], "summary": "unified 1-3 sentence summary", "label": "specific subject label"}
  ]
}

If nothing should be merged, return {"reasoning": "...", "merges": []}.
Stances not listed in any merge group will be left as-is.`,
		personName, stancesBuilder.String())

	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a political fact stance-merge classifier. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		MaxTokens:      4000,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling merge-decision request: %w", err)
	}

	log.Debug().
		Str("person", personName).
		Int("cluster_size", len(cluster)).
		Msg("Sending stance-merge candidate cluster to LLM")

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("LLM merge-decision request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("decoding merge-decision response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return nil, fmt.Errorf("merge-decision LLM returned no content")
	}

	content := stripMarkdownCodeBlock(strings.TrimSpace(chatResp.Choices[0].Message.Content))

	// Parse leniently: we only care about the merges array; the reasoning field
	// is informational.
	var raw struct {
		Reasoning string          `json:"reasoning"`
		Merges    []MergeSubgroup `json:"merges"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse merge-decision response")
		return nil, fmt.Errorf("parsing merge-decision response: %w", err)
	}

	// Validate every subgroup: must reference ≥ 2 known IDs, summary/label non-empty.
	idSet := make(map[int64]bool, len(cluster))
	for _, s := range cluster {
		idSet[s.ID] = true
	}
	var clean []MergeSubgroup
	for _, sg := range raw.Merges {
		if len(sg.StanceIDs) < 2 {
			log.Warn().Interface("subgroup", sg).Msg("Merge-decision: skipping subgroup with <2 stance IDs")
			continue
		}
		seen := make(map[int64]bool, len(sg.StanceIDs))
		valid := true
		for _, id := range sg.StanceIDs {
			if !idSet[id] {
				log.Warn().Int64("unknown_id", id).Interface("subgroup", sg).Msg("Merge-decision: subgroup references stance ID not in cluster")
				valid = false
				break
			}
			if seen[id] {
				log.Warn().Int64("dup_id", id).Interface("subgroup", sg).Msg("Merge-decision: subgroup has duplicate stance ID")
				valid = false
				break
			}
			seen[id] = true
		}
		if !valid {
			continue
		}
		if strings.TrimSpace(sg.Summary) == "" || strings.TrimSpace(sg.Label) == "" {
			log.Warn().Interface("subgroup", sg).Msg("Merge-decision: skipping subgroup with empty summary or label")
			continue
		}
		clean = append(clean, sg)
	}

	if raw.Reasoning != "" {
		log.Info().Str("person", personName).Int("cluster_size", len(cluster)).Int("subgroups", len(clean)).Str("reasoning", raw.Reasoning).Msg("Merge-decision LLM result")
	}
	return &MergeDecision{Merges: clean}, nil
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
