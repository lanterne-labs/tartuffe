package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// StanceStorer defines the storage interface for stance operations.
type StanceStorer interface {
	InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error)
	UpdateStanceDocument(ctx context.Context, id int64, summary, summaryEn string, embedding []float32, keyFactIDs []int64, factCount int) error
	UpdateStanceMetadata(ctx context.Context, id int64, label, labelEn, summary, summaryEn string) error
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error)
	DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error
	RecordContradiction(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) error
}

// StanceDocument is the core type for stance documents.
type StanceDocument struct {
	ID               int64
	PersonID         int64
	ClusterLabel     string
	LabelEn          string // English translation of ClusterLabel; empty if already English
	ClusterEmbedding []float32
	Summary          string
	SummaryEn        string // English translation of Summary; empty if already English
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
	Lang               string // BCP-47 source language ("fr", "en", ...); always set on new inserts.
}

// StanceContradictionEntry is one (new_fact, key_fact) contradiction the LLM
// flagged inside the analyze call. A single ProcessNewFact invocation may
// produce zero or many of these — when many key facts each conflict with the
// new fact in different ways, every pair gets its own entry.
type StanceContradictionEntry struct {
	KeyFactID int64  `json:"key_fact_id"`
	Reason    string `json:"reason"`
	ReasonEn  string `json:"reason_en,omitempty"` // English translation; empty when reason is already English
}

// StanceProcessResult holds the outcome of processing a new fact against existing stances.
type StanceProcessResult struct {
	Verdict        string // "NEW_STANCE", "DUPLICATE", "NEW_INFO", "CONTRADICTION"
	StanceID       int64
	Reasoning      string
	Contradictions []StanceContradictionEntry // empty unless Verdict==CONTRADICTION
}

// stanceLLMResponse is the expected JSON response from the stance analysis LLM call.
// The schema returns ONE primary verdict for the new fact's relationship to the
// stance as a whole, plus a LIST of zero-to-many contradictions (one per key
// fact the new fact conflicts with). The summary is regenerated separately by
// refreshStanceMetadata, not in this call — that keeps the analyze prompt
// focused on classification + contradiction enumeration.
type stanceLLMResponse struct {
	Reasoning      string                     `json:"reasoning"`
	PrimaryVerdict string                     `json:"primary_verdict"` // DUPLICATE | NEW_INFO | CONTRADICTION
	DuplicateOf    int64                      `json:"duplicate_of_key_fact_id"`
	Contradictions []StanceContradictionEntry `json:"contradictions"`
}

// topicAssignmentResponse is the expected JSON response from the topic assignment LLM call.
// NewTopicLabel uses flexibleString because some LLMs (notably gemma-4) occasionally
// emit a number (e.g. 0) for this field when AssignedIndex != 0 (i.e. when no new
// topic is being created). A strict string parse would fail the whole call.
type topicAssignmentResponse struct {
	Reasoning       string         `json:"reasoning"`
	AssignedIndex   int            `json:"assigned_topic_index"`     // 1-indexed, 0 means new topic
	NewTopicLabel   flexibleString `json:"new_topic_label"`          // only set when AssignedIndex == 0
	NewTopicLabelEn flexibleString `json:"new_topic_label_en"`       // English translation; empty when new_topic_label is already English
}

// generalLabelResponse is the response from generateGeneralLabel — a thin
// helper used to recover from cases where assignTopic somehow returned an
// empty NewTopicLabel.
type generalLabelResponse struct {
	Label flexibleString `json:"label"`
}

// stanceMetadataResponse is the response from refreshStanceMetadata — a fresh
// general topic label and a 2-4 sentence summary regenerated from the FULL
// fact list of the stance.
type stanceMetadataResponse struct {
	Label     flexibleString `json:"label"`
	LabelEn   flexibleString `json:"label_en"`   // English translation; empty when label is already English
	Summary   string         `json:"summary"`
	SummaryEn string         `json:"summary_en"` // English translation; empty when summary is already English
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

// keyFactBatchSize caps the number of key facts the analyze call handles per
// LLM invocation. With gemma-4's 12K context, ~3.5K tokens for the rules,
// ~500 tokens for summary + new fact, and an 800-token output cap, we have
// headroom for ~40 facts × ~80 tokens each. Stances larger than this are
// processed across multiple sequential analyze calls; contradictions are
// unioned across batches.
const keyFactBatchSize = 40

// stanceShortlistSize caps the number of existing stances sent to assignTopic
// in a single LLM call. When a person has more than this many stances, we
// pre-filter via embedding cosine similarity (fact embedding vs each stance's
// cluster_embedding) and pass only the top-K closest to the LLM. Embeddings
// are recon; the LLM makes the final assignment decision.
const stanceShortlistSize = 15

// StanceManager handles the core stance document logic: matching new facts to existing
// stances, creating new stances, and detecting contradictions via LLM analysis.
type StanceManager struct {
	storer StanceStorer
	apiKey string
	apiURL string
	model  string
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
// It uses LLM-based topic assignment to find the right stance (or to generate a
// general topic label when no existing stance fits), then analyzes the new fact
// pair-wise against all key facts in the matched stance. After a non-duplicate
// verdict, the stance's label + summary are regenerated from the full fact list.
func (sm *StanceManager) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	stances, err := sm.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("getting stances for person: %w", err)
	}

	// Always go through assignTopic — even when no stances exist, the LLM emits a
	// general topic label per the LABEL RULES instead of falling back to fact text.
	// When the person has many stances, assignTopic uses an embedding-based
	// shortlist so the LLM only sees the top-K closest candidates.
	assignment, err := sm.assignTopic(ctx, stances, factText, embedding)
	if err != nil {
		return nil, fmt.Errorf("topic assignment: %w", err)
	}

	// New topic: create a new stance with the LLM-generated general label
	if assignment.AssignedIndex == 0 {
		label := strings.TrimSpace(string(assignment.NewTopicLabel))
		labelEn := strings.TrimSpace(string(assignment.NewTopicLabelEn))
		if label == "" {
			// Defensive: assignTopic didn't emit a usable label. Ask explicitly via
			// generateGeneralLabel so we never store fact text as a stance label.
			label, err = sm.generateGeneralLabel(ctx, factText)
			if err != nil {
				return nil, fmt.Errorf("generating fallback general label: %w", err)
			}
		}
		log.Info().
			Str("new_topic", label).
			Int64("fact_id", factID).
			Str("reasoning", assignment.Reasoning).
			Msg("LLM assigned fact to new topic")
		return sm.createNewStance(ctx, personID, factID, factText, embedding, label, labelEn)
	}

	// Find the matched stance by index (1-indexed)
	if assignment.AssignedIndex < 1 || assignment.AssignedIndex > len(stances) {
		log.Warn().
			Int("assigned_index", assignment.AssignedIndex).
			Int("total_stances", len(stances)).
			Msg("LLM returned out-of-range topic index, creating new stance")
		label, err := sm.generateGeneralLabel(ctx, factText)
		if err != nil {
			return nil, fmt.Errorf("generating fallback general label: %w", err)
		}
		return sm.createNewStance(ctx, personID, factID, factText, embedding, label, "")
	}
	matched := &stances[assignment.AssignedIndex-1]

	log.Info().
		Int64("stance_id", matched.ID).
		Str("topic", matched.ClusterLabel).
		Int64("fact_id", factID).
		Str("reasoning", assignment.Reasoning).
		Msg("LLM assigned fact to existing topic")

	// Get key facts for the matched stance and analyze relationship.
	keyFacts, err := sm.storer.GetFactsForStance(ctx, matched.ID)
	if err != nil {
		return nil, fmt.Errorf("getting facts for stance %d: %w", matched.ID, err)
	}

	// Add the fact to the stance join table BEFORE analysis. AddFactToStance is
	// idempotent (ON CONFLICT) so a partial/retried call leaves the join row in
	// place; we want the fact to belong to its stance even if a downstream LLM
	// call errors.
	if err := sm.storer.AddFactToStance(ctx, matched.ID, factID); err != nil {
		return nil, fmt.Errorf("adding fact to stance %d: %w", matched.ID, err)
	}

	// Analyze the new fact against the stance's key facts. When there are too
	// many key facts to fit in one prompt, batch the call and union the
	// contradictions across batches.
	agg, err := sm.analyzeAcrossBatches(ctx, matched.Summary, keyFacts, factText)
	if err != nil {
		return nil, fmt.Errorf("LLM stance analysis: %w", err)
	}

	return sm.handleVerdict(ctx, matched, personID, factID, embedding, agg)
}

// aggregatedAnalysis is the cross-batch result of analyzeAcrossBatches.
type aggregatedAnalysis struct {
	Reasoning      string                     // concatenation of per-batch reasoning lines
	PrimaryVerdict string                     // DUPLICATE | NEW_INFO | CONTRADICTION
	DuplicateOf    int64                      // first non-zero duplicate_of_key_fact_id seen
	Contradictions []StanceContradictionEntry // deduped by key_fact_id
}

// analyzeAcrossBatches splits keyFacts into batches of at most keyFactBatchSize
// and calls callLLM once per batch with the same summary + new fact. Across all
// batches the contradictions list is unioned (deduped by KeyFactID). The
// primary verdict is CONTRADICTION if any batch returned contradictions, else
// DUPLICATE if any batch returned DUPLICATE, else NEW_INFO.
func (sm *StanceManager) analyzeAcrossBatches(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string) (*aggregatedAnalysis, error) {
	batches := chunkKeyFacts(keyFacts, keyFactBatchSize)
	if len(batches) == 0 {
		// No key facts at all — degenerate but possible. Treat the new fact as
		// NEW_INFO so the stance gets the new fact attached without a contradiction
		// claim. The refresh call will produce a label/summary from the now-1 fact.
		return &aggregatedAnalysis{PrimaryVerdict: "NEW_INFO"}, nil
	}

	agg := &aggregatedAnalysis{}
	seen := make(map[int64]bool)
	var hasDuplicate, hasNewInfo bool
	var reasoningParts []string

	for batchIdx, batch := range batches {
		resp, err := sm.callLLM(ctx, stanceSummary, batch, newFactText)
		if err != nil {
			return nil, fmt.Errorf("analyze batch %d/%d: %w", batchIdx+1, len(batches), err)
		}
		if resp.Reasoning != "" {
			reasoningParts = append(reasoningParts, resp.Reasoning)
		}
		switch resp.PrimaryVerdict {
		case "DUPLICATE":
			hasDuplicate = true
			if agg.DuplicateOf == 0 && resp.DuplicateOf > 0 {
				agg.DuplicateOf = resp.DuplicateOf
			}
		case "NEW_INFO":
			hasNewInfo = true
		case "CONTRADICTION":
			// fall through to contradiction collection
		}
		for _, c := range resp.Contradictions {
			if c.KeyFactID <= 0 || seen[c.KeyFactID] {
				continue
			}
			seen[c.KeyFactID] = true
			agg.Contradictions = append(agg.Contradictions, c)
		}
	}

	switch {
	case len(agg.Contradictions) > 0:
		agg.PrimaryVerdict = "CONTRADICTION"
	case hasDuplicate:
		agg.PrimaryVerdict = "DUPLICATE"
	case hasNewInfo:
		agg.PrimaryVerdict = "NEW_INFO"
	default:
		// All batches returned something unexpected. Default to NEW_INFO so the
		// fact still gets attached without a spurious contradiction.
		agg.PrimaryVerdict = "NEW_INFO"
	}
	agg.Reasoning = strings.Join(reasoningParts, " | ")
	return agg, nil
}

// chunkKeyFacts splits the key-facts slice into batches of at most size each.
// Returns nil if the input is empty.
func chunkKeyFacts(facts []StanceFactInfo, size int) [][]StanceFactInfo {
	if size <= 0 {
		size = keyFactBatchSize
	}
	if len(facts) == 0 {
		return nil
	}
	var out [][]StanceFactInfo
	for i := 0; i < len(facts); i += size {
		end := i + size
		if end > len(facts) {
			end = len(facts)
		}
		out = append(out, facts[i:end])
	}
	return out
}

// createNewStance creates a new stance document for a fact. The label MUST be
// non-empty — callers route through assignTopic / generateGeneralLabel to
// obtain a general topic label. If the supplied label is empty we error rather
// than falling back to fact text (which historically produced quoted-statement
// labels that fragmented stances).
func (sm *StanceManager) createNewStance(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, label, labelEn string) (*StanceProcessResult, error) {
	label = strings.TrimSpace(label)
	labelEn = strings.TrimSpace(labelEn)
	if label == "" {
		// Last-ditch defensive call — shouldn't happen given ProcessNewFact's flow,
		// but if it does, generate a label rather than store fact text.
		generated, err := sm.generateGeneralLabel(ctx, factText)
		if err != nil {
			return nil, fmt.Errorf("createNewStance: missing label and generateGeneralLabel failed: %w", err)
		}
		label = generated
	}
	if r := []rune(label); len(r) > 120 {
		label = string(r[:120])
	}
	if r := []rune(labelEn); len(r) > 120 {
		labelEn = string(r[:120])
	}

	doc := StanceDocument{
		PersonID:         personID,
		ClusterLabel:     label,
		LabelEn:          labelEn,
		ClusterEmbedding: embedding,
		Summary:          factText,
		// SummaryEn intentionally empty at creation — first triggerStanceRefresh
		// (fired when the second fact joins this stance) regenerates both summary
		// and summary_en bilingually. Until then the frontend falls back to native.
		KeyFactIDs: []int64{factID},
		FactCount:  1,
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

// handleVerdict processes the aggregated analyze result, updates the stance
// counters/centroid/key_facts as appropriate, records every contradiction the
// LLM flagged, and triggers a label+summary refresh for non-duplicate verdicts.
func (sm *StanceManager) handleVerdict(ctx context.Context, matched *StanceDocument, personID int64, factID int64, embedding []float32, agg *aggregatedAnalysis) (*StanceProcessResult, error) {
	result := &StanceProcessResult{
		StanceID:  matched.ID,
		Reasoning: agg.Reasoning,
	}

	switch agg.PrimaryVerdict {
	case "DUPLICATE":
		result.Verdict = "DUPLICATE"
		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int64("duplicate_of", agg.DuplicateOf).
			Str("reasoning", agg.Reasoning).
			Msg("Fact is duplicate of existing stance")

	case "NEW_INFO":
		result.Verdict = "NEW_INFO"
		if err := sm.applyStanceUpdate(ctx, matched, factID, embedding); err != nil {
			return nil, err
		}
		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("new_fact_count", matched.FactCount+1).
			Msg("Added new info to existing stance")

	case "CONTRADICTION":
		result.Verdict = "CONTRADICTION"
		result.Contradictions = agg.Contradictions

		recorded := sm.recordStanceContradictions(ctx, personID, factID, matched.ID, agg.Contradictions)

		if err := sm.applyStanceUpdate(ctx, matched, factID, embedding); err != nil {
			return nil, err
		}

		log.Warn().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("contradictions_recorded", recorded).
			Int("contradictions_total", len(agg.Contradictions)).
			Str("reasoning", agg.Reasoning).
			Msg("Contradictions detected in stance")

	default:
		return nil, fmt.Errorf("unexpected LLM verdict: %q", agg.PrimaryVerdict)
	}

	// For non-duplicate verdicts the stance content has materially changed —
	// regenerate label + summary from the full fact list. Failures here are
	// logged but do not fail the verdict (the stance is still correct, just
	// using its prior label/summary until the next refresh).
	if result.Verdict == "NEW_INFO" || result.Verdict == "CONTRADICTION" {
		if err := sm.triggerStanceRefresh(ctx, matched.ID); err != nil {
			log.Warn().Err(err).
				Int64("stance_id", matched.ID).
				Msg("Stance metadata refresh failed; stance kept with previous label/summary")
		}
	}

	return result, nil
}

// applyStanceUpdate writes the centroid + fact count + key fact IDs for a
// stance after a fact has joined. Summary is left as the existing one — the
// refresh call (triggerStanceRefresh) is responsible for regenerating it.
func (sm *StanceManager) applyStanceUpdate(ctx context.Context, matched *StanceDocument, factID int64, embedding []float32) error {
	newCentroid := updateCentroid(matched.ClusterEmbedding, embedding, matched.FactCount)
	newFactCount := matched.FactCount + 1
	keyFactIDs := matched.KeyFactIDs
	if len(keyFactIDs) < 10 {
		keyFactIDs = append(keyFactIDs, factID)
	}
	return sm.storer.UpdateStanceDocument(ctx, matched.ID, matched.Summary, matched.SummaryEn, newCentroid, keyFactIDs, newFactCount)
}

// AnalyzeFactInStance treats `fact` as if it just arrived in `stance` and runs
// the analyze + multi-verdict + contradiction-recording path against
// `otherFacts`. Used by the post-merge sweep to detect contradictions between
// facts that were previously separated across different stances. Returns the
// number of contradictions recorded.
//
// Unlike ProcessNewFact this skips assignTopic (we already KNOW the stance the
// fact belongs to — no need to re-classify) and skips the centroid / fact_count
// / key_fact_ids update (the merge already handled that). It does NOT trigger a
// metadata refresh — callers should call RefreshStanceMetadata once after all
// per-fact analysis is done, to avoid N redundant refresh calls.
func (sm *StanceManager) AnalyzeFactInStance(
	ctx context.Context,
	personID int64,
	stance StanceDocument,
	fact StanceFactInfo,
	otherFacts []StanceFactInfo,
) (int, error) {
	if len(otherFacts) == 0 {
		return 0, nil
	}
	agg, err := sm.analyzeAcrossBatches(ctx, stance.Summary, otherFacts, fact.NormalizedFactText)
	if err != nil {
		return 0, fmt.Errorf("analyze: %w", err)
	}
	if agg.PrimaryVerdict != "CONTRADICTION" {
		return 0, nil
	}
	return sm.recordStanceContradictions(ctx, personID, fact.ID, stance.ID, agg.Contradictions), nil
}

// recordStanceContradictions writes each contradiction entry to the store,
// skipping invalid entries (KeyFactID <= 0) and self-pairs (KeyFactID == factID).
// Per-entry RecordContradiction errors are logged but do not abort the loop.
// Returns the number successfully recorded.
//
// Shared by handleVerdict (live ingest path) and AnalyzeFactInStance
// (post-merge sweep) so both treat malformed LLM output the same way.
func (sm *StanceManager) recordStanceContradictions(
	ctx context.Context,
	personID int64,
	factID int64,
	stanceID int64,
	entries []StanceContradictionEntry,
) int {
	recorded := 0
	for _, c := range entries {
		if c.KeyFactID <= 0 {
			log.Warn().
				Int64("stance_id", stanceID).
				Int64("fact_id", factID).
				Msg("Contradiction entry with invalid key_fact_id, skipping")
			continue
		}
		if c.KeyFactID == factID {
			// Self-pair — should never come from the LLM but guard anyway.
			continue
		}
		if err := sm.storer.RecordContradiction(ctx, personID, factID, c.KeyFactID, c.Reason, c.ReasonEn); err != nil {
			log.Warn().Err(err).
				Int64("stance_id", stanceID).
				Int64("fact_a", factID).
				Int64("fact_b", c.KeyFactID).
				Msg("Failed to record contradiction; continuing")
			continue
		}
		recorded++
	}
	return recorded
}

// RefreshStanceMetadata is a public wrapper around triggerStanceRefresh, used
// by code outside ProcessNewFact (e.g., the post-merge sweep) that needs to
// regenerate a stance's label + summary from the full fact list.
func (sm *StanceManager) RefreshStanceMetadata(ctx context.Context, stanceID int64) error {
	return sm.triggerStanceRefresh(ctx, stanceID)
}

// triggerStanceRefresh loads the current full fact set for the stance, calls
// the LLM to regenerate a general label + 2-4 sentence summary, and persists
// both via UpdateStanceMetadata.
func (sm *StanceManager) triggerStanceRefresh(ctx context.Context, stanceID int64) error {
	facts, err := sm.storer.GetFactsForStance(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading facts for refresh: %w", err)
	}
	if len(facts) == 0 {
		return nil // nothing to refresh from
	}

	label, labelEn, summary, summaryEn, err := sm.refreshStanceMetadata(ctx, stanceID, facts)
	if err != nil {
		return fmt.Errorf("regenerating metadata: %w", err)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("refresh returned empty label for stance %d", stanceID)
	}
	if r := []rune(label); len(r) > 120 {
		label = string(r[:120])
	}
	if r := []rune(labelEn); len(r) > 120 {
		labelEn = string(r[:120])
	}
	if err := sm.storer.UpdateStanceMetadata(ctx, stanceID, label, labelEn, summary, summaryEn); err != nil {
		return fmt.Errorf("persisting refreshed metadata: %w", err)
	}
	log.Debug().Int64("stance_id", stanceID).Str("new_label", label).Int("facts", len(facts)).Msg("Refreshed stance metadata")
	return nil
}

// callJSONLLM is the shared boilerplate for sending a JSON-output chat request
// to the LLM. Returns the assistant's response content with markdown code-block
// fences stripped. Callers json.Unmarshal the returned content into their own
// response type.
//
// Used by callLLM, assignTopic, generateGeneralLabel, refreshStanceMetadata,
// and MergeClusterDecision — five sites that share the same request envelope
// (system + user message, JSON response format, configurable temperature +
// max tokens) and the same response shape (first chat choice → strip fences).
func (sm *StanceManager) callJSONLLM(
	ctx context.Context,
	systemMsg, userPrompt string,
	temperature float64,
	maxTokens int,
) (string, error) {
	reqPayload := llm.ChatRequest{
		Model: sm.model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: systemMsg},
			{Role: "user", Content: userPrompt},
		},
		Temperature:    temperature,
		MaxTokens:      maxTokens,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshaling LLM request: %w", err)
	}

	bodyBytes, err := sm.client.DoRequest(ctx, sm.apiURL, sm.apiKey, payloadBytes)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("decoding chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("LLM returned no content")
	}

	return stripMarkdownCodeBlock(strings.TrimSpace(chatResp.Choices[0].Message.Content)), nil
}

// callLLM sends the stance analysis prompt for ONE batch of key facts to the LLM
// and parses the response. Callers split into batches via chunkKeyFacts; this
// function handles a single batch and returns whatever contradictions the LLM
// found within it.
func (sm *StanceManager) callLLM(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string) (*stanceLLMResponse, error) {
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

Decide ONE primary_verdict for the NEW FACT vs the stance:
1. DUPLICATE — the new fact says essentially the same thing as one of the key facts (paraphrase / same claim, different source). Set duplicate_of_key_fact_id to that key fact's [ID N].
2. NEW_INFO — the new fact adds new information to this topic without contradicting any key fact.
3. CONTRADICTION — the new fact contradicts ONE OR MORE specific key facts per the rules below. List EVERY contradicting key fact in the contradictions array — not just one. If the new fact contradicts both key fact 123 AND key fact 456 in different ways, return two entries.

`+promptrules.ContradictionRules+`

**HOW TO MAP THE PAIR-WISE EXAMPLES TO THIS TASK:**
The examples above are framed as "PAIR N: YES/NO". Treat YES as a contradiction entry (one per contradicted key fact). Treat NO as either DUPLICATE (same position, different words — set duplicate_of_key_fact_id) or NEW_INFO (compatible-but-distinct).

**RESPONSE FORMAT:**
Compare the NEW FACT against EACH key fact independently before deciding the primary verdict. Write your reasoning BEFORE listing contradictions.

**LANGUAGE RULE:** Write each contradiction's ` + "`reason`" + ` in the SAME LANGUAGE as the KEY FACTS and NEW FACT (if facts span multiple languages, use French). For EACH contradiction whose ` + "`reason`" + ` is NOT English, ALSO provide ` + "`reason_en`" + ` — a faithful English translation. OMIT ` + "`reason_en`" + ` when ` + "`reason`" + ` is already English. ` + "`reasoning`" + ` (the top-level analysis field) follows the same language rule.

Return ONLY valid JSON:
{
  "reasoning": "1-3 sentences: identify the new fact's core claim, list each key fact it conflicts with (and why), then state the primary verdict",
  "primary_verdict": "DUPLICATE|NEW_INFO|CONTRADICTION",
  "duplicate_of_key_fact_id": 0,
  "contradictions": [
    {"key_fact_id": <id>, "reason": "1-2 sentences explaining the contradiction", "reason_en": "English translation (omit when reason is English)"}
  ]
}

For DUPLICATE: contradictions MUST be []. Set duplicate_of_key_fact_id to the [ID N] of the key fact the new fact paraphrases.
For NEW_INFO: contradictions MUST be []. Set duplicate_of_key_fact_id to 0.
For CONTRADICTION: contradictions MUST contain at least one entry; set duplicate_of_key_fact_id to 0.`,
		stanceSummary, factsBuilder.String(), newFactText)

	// temp=0.1 matches the rest of the codebase (every other contradiction /
	// detector / auditor / dedup path uses 0.1 or lower); the 0.2 default that
	// lived here was the lone outlier and the source of intermittent
	// false-positive contradictions at stance-insert time. Repro at temp 0.0,
	// 0.1, 0.2 × 10 trials each all returned NEW_INFO on the AfD-Saxony case
	// that fired in prod, so the variance was the only factor and 0.1 keeps a
	// tiny exploration margin without the misfires.
	content, err := sm.callJSONLLM(ctx, "You are a political fact analysis assistant. Return only valid JSON.", prompt, 0.1, 800)
	if err != nil {
		return nil, fmt.Errorf("stance analysis: %w", err)
	}
	resp, err := parseStanceLLMResponse(content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse stance LLM response")
		return nil, fmt.Errorf("parsing stance LLM response: %w", err)
	}
	return resp, nil
}

// assignTopic asks the LLM to assign a new fact to one of the existing stance
// topics or declare it new. Even with an empty stances list the LLM is forced
// to AssignedIndex=0 and emits a general topic label per the LABEL RULES — that
// path replaces the old "use truncated fact text as label" shortcut.
//
// When the person has more than stanceShortlistSize stances, we pre-filter via
// embedding cosine similarity (fact embedding vs each stance's centroid) and
// pass only the top-K closest stances to the LLM. The LLM still makes the
// final decision; embeddings are recon. Returned AssignedIndex is mapped back
// to the original stances slice's 1-indexed positions.
func (sm *StanceManager) assignTopic(ctx context.Context, stances []StanceDocument, factText string, factEmbedding []float32) (*topicAssignmentResponse, error) {
	shortlist, indexMap := shortlistStancesByEmbedding(stances, factEmbedding, stanceShortlistSize)

	var topicsBuilder strings.Builder
	if len(shortlist) == 0 {
		topicsBuilder.WriteString("(no existing topics — you MUST return assigned_topic_index=0 and emit a general new_topic_label per the LABEL RULES below)\n")
	} else {
		for i, s := range shortlist {
			topicsBuilder.WriteString(fmt.Sprintf("%d. %q (%d facts): %s\n",
				i+1, s.ClusterLabel, s.FactCount, truncate(s.Summary, 150)))
		}
	}

	prompt := fmt.Sprintf(`You are a topic classifier for political facts. Given a new fact and a list of existing topics about a person, decide which topic the fact belongs to.

EXISTING TOPICS:
%s
NEW FACT:
%s

ASSIGNMENT RULES (lean toward consolidation, not fragmentation):
- A topic is a SUBJECT AREA, not a specific event. "Trump on Iran" covers the Iran coalition, the pilot rescue, Operation Epic Fury, and Iran-related media remarks — they are all the same topic.
- Assign the fact to an existing topic if it shares the SAME subject area. Different events / different details / different aspects of the same subject area all belong together.
- Examples that should consolidate:
  * "sentencing in PA case" + "denial of wrongdoing in PA case" + "comments on bracelet condition" → all one "presidential candidacy and PA case" topic.
  * "Iran sanctions threats" + "Iran nuclear talks" + "Iran-Saudi attack confirmation" → all one "Iran policy" topic.
  * "EU debt criticism" + "euro inadapté" + "demanding to leave the EU" → all one "EU economic policy" topic.
- Only create a NEW topic when the fact is about a genuinely different SUBJECT AREA that is not represented at all by any existing topic.
- When in doubt, ALWAYS prefer assigning to the closest existing topic over creating a new one.

LABEL RULES (for new_topic_label when creating a new topic):
- Describe the SUBJECT AREA broadly enough that 5-20 related facts could naturally fit under this label.
- Format: "{subject area}{ optional temporal scope}". Examples:
  * "Iran policy and military operations"
  * "presidential candidacy 2027"
  * "EU economic and trade policy"
  * "nuclear deterrence policy"
  * "parliamentary assistants case"
  * "municipal elections 2026"
- Do NOT include the person's name (labels are already per-person).
- **LANGUAGE:** Write ` + "`new_topic_label`" + ` in the same language as the NEW FACT provided. Do not translate to English unless the fact itself is in English. If facts span multiple languages, use French.
- ⚠️ ABSOLUTELY NEVER quote a sentence, phrase, or specific verb the person said. Labels describe topics, not statements. Examples of FORBIDDEN labels:
  * ❌ "Donald Trump a posté « FREE LEPEN »" — quoting an action
  * ❌ "Marine Le Pen a affirmé qu'elle ne se présenterait pas..." — quoting a statement
  * ❌ "denied bracelet candidacy" — quoting a verb + claim
- ⚠️ Do NOT pack event-detail into the label. The label should NOT name a specific ruling, date, or instance.
  * ❌ "presidential candidacy eligibility and legal sentencing 2027 first-instance bracelet ruling"
  * ✅ "presidential candidacy 2027"
- ⚠️ NEVER use a META / PATTERN-OF-BEHAVIOR / RHETORICAL label. The label must name a SUBJECT, not a way of speaking. ❌ "policy announcements contradictions" / "campaign promises" / "denials and reversals" / "flip-flops on foreign policy". ✅ "US tariffs on Chinese goods" / "Strait of Hormuz coalition".
- If the fact is itself a journalist's META observation about the entity, the upstream extractor should have rejected it; either pick the closest existing subject-bound topic or, if truly no fit, emit a SUBJECT-bound new label (never the meta phrasing).

Return ONLY valid JSON:
{
  "reasoning": "Brief explanation of your choice",
  "assigned_topic_index": 0,
  "new_topic_label": "broad subject-area label, max 120 chars (only if assigned_topic_index is 0)",
  "new_topic_label_en": "faithful English translation of new_topic_label — OMIT when new_topic_label is already English"
}

Set assigned_topic_index to the number of the matching topic (1-%d), or 0 if no existing topic fits.`, topicsBuilder.String(), factText, len(shortlist))

	content, err := sm.callJSONLLM(ctx, "You are a political fact topic classifier. Return only valid JSON.", prompt, 0.1, 800)
	if err != nil {
		return nil, fmt.Errorf("topic assignment: %w", err)
	}

	var resp topicAssignmentResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse topic assignment response")
		return nil, fmt.Errorf("parsing topic assignment response: %w", err)
	}

	// Translate the LLM's shortlist-relative index back to the original
	// stances-slice index so callers can subscript stances[AssignedIndex-1]
	// transparently.
	if indexMap != nil && resp.AssignedIndex >= 1 && resp.AssignedIndex <= len(shortlist) {
		resp.AssignedIndex = indexMap[resp.AssignedIndex-1] + 1
	}

	return &resp, nil
}

// shortlistStancesByEmbedding pre-filters a stances slice down to the top-K
// closest stances by cosine similarity (fact embedding vs stance centroid).
// Returns the shortlist plus an indexMap such that indexMap[i] is the original
// position in stances of shortlist[i]. When stances has ≤ K entries (or the
// fact has no embedding), returns the input unchanged with indexMap=nil
// (signaling "no remapping needed").
//
// All embeddings are L2-normalised so dot product == cosine similarity.
func shortlistStancesByEmbedding(stances []StanceDocument, factEmbedding []float32, k int) ([]StanceDocument, []int) {
	if k <= 0 || len(stances) <= k || len(factEmbedding) == 0 {
		return stances, nil
	}
	type entry struct {
		origIdx int
		sim     float64
	}
	scored := make([]entry, 0, len(stances))
	for i, s := range stances {
		if len(s.ClusterEmbedding) != len(factEmbedding) {
			// Mismatched dimensions — exclude rather than crash.
			continue
		}
		var sim float64
		for j := range factEmbedding {
			sim += float64(factEmbedding[j]) * float64(s.ClusterEmbedding[j])
		}
		scored = append(scored, entry{origIdx: i, sim: sim})
	}
	if len(scored) <= k {
		// Nothing usable was filtered out; if scored < stances we'd lose the
		// excluded ones, but in practice all centroids share the embedding dim
		// so this is fine.
		shortlist := make([]StanceDocument, len(scored))
		idxMap := make([]int, len(scored))
		for i, e := range scored {
			shortlist[i] = stances[e.origIdx]
			idxMap[i] = e.origIdx
		}
		return shortlist, idxMap
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].sim > scored[j].sim })
	scored = scored[:k]
	shortlist := make([]StanceDocument, k)
	idxMap := make([]int, k)
	for i, e := range scored {
		shortlist[i] = stances[e.origIdx]
		idxMap[i] = e.origIdx
	}
	return shortlist, idxMap
}

// generateGeneralLabel produces a general subject-area label for a single fact.
// Used as a defensive fallback in createNewStance and ProcessNewFact when
// assignTopic somehow returns an empty NewTopicLabel — keeps the system from
// ever falling back to the fact text as a stance label.
func (sm *StanceManager) generateGeneralLabel(ctx context.Context, factText string) (string, error) {
	prompt := fmt.Sprintf(`Generate a single GENERAL topic label for the political fact below. The label describes the SUBJECT AREA broadly enough that 5-20 related future facts could naturally fit under it.

FACT:
%s

LABEL RULES:
- Format: "{subject area}{ optional temporal scope}". Examples: "Iran policy", "presidential candidacy 2027", "EU economic policy", "nuclear deterrence policy", "parliamentary assistants case".
- Do NOT include the person's name.
- ABSOLUTELY NEVER quote a sentence, phrase, or specific verb from the fact. Describe the topic, not the statement.
- Do NOT pack event-detail into the label.
- NEVER use a meta / pattern-of-behavior / rhetorical label.

Return ONLY valid JSON:
{"label": "subject-area label, max 120 chars"}`, factText)

	content, err := sm.callJSONLLM(ctx, "You generate broad topic labels for political facts. Return only valid JSON.", prompt, 0.1, 200)
	if err != nil {
		return "", fmt.Errorf("label generation: %w", err)
	}
	var resp generalLabelResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return "", fmt.Errorf("parsing label response: %w", err)
	}
	label := strings.TrimSpace(string(resp.Label))
	if label == "" {
		return "", fmt.Errorf("label LLM returned empty label")
	}
	return label, nil
}

// refreshStanceMetadata regenerates a stance's cluster_label AND summary from
// the FULL set of facts currently attached to it. Called from triggerStanceRefresh
// after every non-duplicate verdict so the stance metadata stays in sync with
// its fact set.
//
// The prompt asks the LLM to:
//   - Re-derive the broadest accurate subject-area label per the LABEL RULES.
//   - Write a 2-4 sentence summary that PRESERVES tensions if any pair of key
//     facts conflict — do not smooth contradictions into one narrative.
func (sm *StanceManager) refreshStanceMetadata(ctx context.Context, stanceID int64, facts []StanceFactInfo) (string, string, string, string, error) {
	if len(facts) == 0 {
		return "", "", "", "", fmt.Errorf("refreshStanceMetadata: stance %d has no facts", stanceID)
	}
	var factsBuilder strings.Builder
	for _, f := range facts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`You are regenerating the topic label and summary for a stance document about a political figure. The stance contains the facts below; produce a fresh broad subject-area label and a 2-4 sentence summary that accurately reflects what's in the stance NOW.

FACTS IN THIS STANCE (each line is "[ID N] (verb) text"):
%s

LABEL RULES:
- The label describes the SUBJECT AREA broadly enough that all listed facts naturally fit under it. Examples: "Iran policy and military operations", "presidential candidacy 2027", "EU economic and trade policy", "nuclear deterrence policy", "parliamentary assistants case".
- Format: "{subject area}{ optional temporal scope}". Max 120 chars.
- Do NOT include the person's name.
- ABSOLUTELY NEVER quote a sentence, phrase, or specific verb from any fact.
- Do NOT pack event-detail into the label.
- NEVER use a meta / pattern-of-behavior / rhetorical label ("denials and reversals", "flip-flops", etc.).

SUMMARY RULES:
- 2-4 sentences. Describe what the person has said / done in this subject area, grounded in the listed facts.
- If two or more key facts CONFLICT (e.g. one promise and one walk-back), the summary MUST PRESERVE THE TENSION — do not smooth it into a unified narrative. Surface that the position has shifted / been walked back / has internal conflict.
- Do not add facts or claims beyond what's in the listed facts.

LANGUAGE RULES:
- Write "label" and "summary" in the SAME LANGUAGE as the listed facts (if facts span multiple languages, use French).
- ALSO provide "label_en" and "summary_en" — faithful English translations. OMIT both _en fields entirely when the native versions are already in English.

Return ONLY valid JSON:
{"label": "subject-area label", "label_en": "English translation (omit if label is English)", "summary": "2-4 sentence summary", "summary_en": "English translation (omit if summary is English)"}`, factsBuilder.String())

	content, err := sm.callJSONLLM(ctx, "You regenerate stance metadata for political facts. Return only valid JSON.", prompt, 0.2, 700)
	if err != nil {
		return "", "", "", "", fmt.Errorf("metadata refresh: %w", err)
	}
	var resp stanceMetadataResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return "", "", "", "", fmt.Errorf("parsing refresh response: %w", err)
	}
	label := strings.TrimSpace(string(resp.Label))
	labelEn := strings.TrimSpace(string(resp.LabelEn))
	summary := strings.TrimSpace(resp.Summary)
	summaryEn := strings.TrimSpace(resp.SummaryEn)
	return label, labelEn, summary, summaryEn, nil
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

	switch resp.PrimaryVerdict {
	case "DUPLICATE", "NEW_INFO", "CONTRADICTION":
		// valid
	default:
		return nil, fmt.Errorf("invalid primary_verdict %q: must be DUPLICATE, NEW_INFO, or CONTRADICTION", resp.PrimaryVerdict)
	}

	// CONTRADICTION must have at least one entry; if not, downgrade to NEW_INFO
	// (the LLM said "contradiction" but listed no specifics — the safer call is
	// to attach the fact as new info rather than record a contradiction with no
	// counterpart).
	if resp.PrimaryVerdict == "CONTRADICTION" && len(resp.Contradictions) == 0 {
		resp.PrimaryVerdict = "NEW_INFO"
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

**LANGUAGE RULE:** Write the unified ` + "`summary`" + ` and ` + "`label`" + ` in the same language as the stances provided. If stances span multiple languages, use French.

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

	content, err := sm.callJSONLLM(ctx, "You are a political fact stance-merge classifier. Return only valid JSON.", prompt, 0.1, 800)
	if err != nil {
		return nil, fmt.Errorf("merge decision: %w", err)
	}

	var raw struct {
		Reasoning string          `json:"reasoning"`
		Merges    []MergeSubgroup `json:"merges"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse merge-decision response")
		return nil, fmt.Errorf("parsing merge-decision response: %w", err)
	}

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
