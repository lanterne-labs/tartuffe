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
	Reasoning string `json:"reasoning,omitempty"` // per-pair chain-of-thought: why these two specific facts are directly opposed (not user-facing; logged for debugging)
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
		client: llm.NewBaseLLMClient("stance-manager"),
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
	if (result.Verdict == "NEW_INFO" || result.Verdict == "CONTRADICTION") && !refreshDeferred(ctx) {
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

// deferRefreshKey is the context key used by WithDeferRefresh to tell
// handleVerdict to skip the per-fact refresh inside ProcessNewFact. Callers
// processing many facts in a row (the rebuild/backfill loop) opt in so each
// touched stance is refreshed ONCE at the end of the batch rather than after
// every NEW_INFO / CONTRADICTION verdict — a typical heavy person with 40
// facts in one stance goes from 40 refresh calls to 1.
type deferRefreshKey struct{}

// WithDeferRefresh returns a derived context that disables the inline
// triggerStanceRefresh call inside handleVerdict. The caller becomes
// responsible for calling RefreshStanceMetadata on every touched stance at
// the end of the batch.
//
// Safe to call on a context already wrapped with WithDeferRefresh — idempotent.
// Live ingest (FactInserter) does NOT wrap the context, so refresh remains
// inline for new-fact-via-news/twitter/frpolitics flows.
func WithDeferRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferRefreshKey{}, true)
}

// refreshDeferred returns true when the context was wrapped via WithDeferRefresh.
func refreshDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(deferRefreshKey{}).(bool)
	return v
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
3. CONTRADICTION — the new fact DIRECTLY contradicts ONE OR MORE specific key facts per the rules below.

**COMPLETENESS REQUIREMENT (THIS IS CRITICAL — read before anything else):**
You MUST evaluate the NEW FACT against EVERY key fact in the stance, INDEPENDENTLY, and list EVERY key fact that is directly contradicted as a SEPARATE entry in the contradictions array. Do NOT deduplicate based on:
- Similarity between key facts (if three key facts each separately contradict the new fact, return three entries)
- Perceived "strength" of one contradiction over another (a weaker direct contradiction is still a contradiction; list it)
- Same underlying axis or rule (multiple key facts on the same axis can each independently contradict the new fact)
The downstream system builds a contradiction graph from these entries — missing edges silently corrupt the honesty score and hide patterns from users. Err on the side of listing more direct edges, never fewer.

**COMPLETENESS DOES NOT OVERRIDE THE NEGATIVE-PATTERN CARVE-OUTS.** Completeness only fires on pairs that pass the contradiction rules below. If a pair falls under a NEGATIVE PATTERN (ADMINISTRATIVE ERROR vs CRIMINAL INTENT — PAIR 16/17 in the examples; COHERENT DEFENSE — PAIR 18; CONSISTENT IGNORANCE; SAME POSITION IN DIFFERENT WORDS; ANNOUNCEMENT FOLLOWED BY ACTION; etc.), DO NOT emit an entry for it — even if every other heuristic says "but they sound opposite". The carve-outs win.

Specifically:
- "Acknowledged things could have been more rigorous" / "an administrative error was made" / "acted in good faith despite negligence" + "denied any wrongdoing / denied criminal intent" → NOT a contradiction (PAIR 16/17). Do NOT add this edge under completeness.
- Multiple "I didn't know X" / "I didn't know Y" denials about different aspects of the same case → NOT contradictory (CONSISTENT IGNORANCE / COHERENT DEFENSE — PAIR 17/18). Do NOT add edges between them.
- A promise to do X + later actually doing X → fulfilled commitment, NOT contradictory (ANNOUNCEMENT THEN ACTION — PAIR 24).
- Paraphrases of the same position → NOT contradictory (SAME POSITION IN DIFFERENT WORDS — PAIR 20).

Example of correct multi-edge output: if the NEW FACT is "authorised offshoring of a French factory to China" and the stance contains three earlier key facts — [ID 12] "promised to rebuild French artisanat", [ID 17] "voted for tariffs on Chinese imports", [ID 23] "defending French industrial jobs in a Lille speech" — each of those three contradicts the new fact INDEPENDENTLY (each is its own direct opposition between two statements). Return THREE contradiction entries, one per pair.

Counter-example (completeness with a carve-out): if the NEW FACT is "denied any criminal wrongdoing in the parliamentary assistants case" and the stance contains key facts — [ID 50] "acknowledged that the management could have been more rigorous", [ID 51] "court of appeal convicted her for the same case" — only [ID 51] is a contradiction (CLAIM ↔ AUTHORITATIVE FINDING). [ID 50] is NOT a contradiction (CONVICTION vs ACKNOWLEDGMENT OF IMPERFECTIONS carve-out, PAIR 17). Return ONE contradiction entry, not two. Completeness does not force you to add the carved-out pair.

**DIRECT-EDGE REQUIREMENT (the precision brake — apply to each candidate entry):**
Every contradiction entry MUST be a DIRECT logical opposition between the NEW FACT and the named key fact — the pair must be one you would flag as contradictory if you saw only those two statements side by side. Surrounding key facts may be used to verify that a stated CONDITION has been met or a TRIGGER has fired (PAIR 5b / Commitment vs Carve-Out style — the other facts establish that the condition is real, but the contradiction edge is still between the two named statements alone). They may NOT be used to bridge two facts that are themselves aligned. If the new fact and a key fact both move in the SAME DIRECTION on the same subject (both commit to outcome X, both deny outcome Y, both defend the same value), they CANNOT contradict each other — even if a third key fact in the stance moves in the opposite direction. In that case the third key fact contradicts each of them separately; the two aligned facts do not contradict each other.

**Mandatory mental check before emitting each contradiction entry:** ask "If I only had the NEW FACT and this one key fact in front of me, with no other context, would I still flag them as contradictory?"
- If YES → emit the entry (regardless of how many other entries you've already emitted; completeness wins).
- If NO — if you only see the contradiction because of a third key fact — DROP the entry. The third key fact may be its own contradiction with the new fact (record THAT pair instead).

The COMPLETENESS and DIRECT-EDGE rules are complementary: COMPLETENESS says "don't drop direct edges to avoid redundancy", DIRECT-EDGE says "don't add transitive edges to seem thorough". Both together: list every pair the mental check says YES on, no more, no less.

`+promptrules.ContradictionRules+`

**HOW TO MAP THE PAIR-WISE EXAMPLES TO THIS TASK:**
The examples above are framed as "PAIR N: YES/NO". Treat YES as a contradiction entry (one per DIRECTLY contradicted key fact — see DIRECT-EDGE REQUIREMENT above). Treat NO as either DUPLICATE (same position, different words — set duplicate_of_key_fact_id) or NEW_INFO (compatible-but-distinct).

**RESPONSE FORMAT:**
Compare the NEW FACT against EACH key fact independently before deciding the primary verdict. For each contradiction entry, write its per-pair reasoning BEFORE its verdict — and apply the mental check from the DIRECT-EDGE REQUIREMENT to that entry.

**LANGUAGE RULE:** Write each contradiction's ` + "`reason`" + ` in the SAME LANGUAGE as the KEY FACTS and NEW FACT (if facts span multiple languages, use French). For EACH contradiction whose ` + "`reason`" + ` is NOT English, ALSO provide ` + "`reason_en`" + ` — a faithful English translation. OMIT ` + "`reason_en`" + ` when ` + "`reason`" + ` is already English. ` + "`reasoning`" + ` fields (both top-level and per-contradiction) follow the same language rule.

Return ONLY valid JSON:
{
  "reasoning": "1-2 sentences justifying the PRIMARY VERDICT (DUPLICATE / NEW_INFO / CONTRADICTION) for this new fact against the stance as a whole — NOT a list of every contradiction (those have their own reasoning fields below)",
  "primary_verdict": "DUPLICATE|NEW_INFO|CONTRADICTION",
  "duplicate_of_key_fact_id": 0,
  "contradictions": [
    {
      "key_fact_id": <id>,
      "reasoning": "1-2 sentences: restate the NEW FACT's core claim, restate THIS key fact's core claim, then explain why these two are directly opposed when considered in isolation. If you find yourself naming a third key fact's ID to explain the opposition, the DIRECT-EDGE REQUIREMENT fails — drop this entry.",
      "reason": "1-sentence user-facing summary of why these two facts contradict",
      "reason_en": "English translation (omit when reason is English)"
    }
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
	// MaxTokens bumped from 800 → 2000: the per-contradiction `reasoning` field
	// (added with the DIRECT-EDGE REQUIREMENT) makes JSON outputs longer when
	// many key facts are involved. 800 was triggering mid-JSON truncation on
	// Gemma when stances grew past ~15 facts (observed: response stopped at
	// `{"reasoning:`).
	// One retry on JSON-parse failure with a tiny temperature bump. Small models
	// (notably gemma-4 26b) occasionally get stuck in a token-repetition loop on
	// long prompts at low T, producing JSON that runs out of tokens mid-string
	// (e.g. reasoning: "de l'intérêt de l'intérêt de l'intérêt..." until cutoff).
	// A T=0.3 retry consistently breaks the loop because the alternative-token
	// probability becomes non-negligible. If both attempts fail we surface the
	// error to the caller — the fact is then rolled back upstream, which is the
	// right behavior (better to lose one fact than write a corrupt stance edge).
	resp, err := sm.callStanceJSONWithRetry(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("stance analysis: %w", err)
	}
	return resp, nil
}

// callStanceJSONWithRetry runs the stance analyzer prompt with one retry on
// JSON-parse failure. See the comment at the call site for the rationale.
func (sm *StanceManager) callStanceJSONWithRetry(ctx context.Context, prompt string) (*stanceLLMResponse, error) {
	const sys = "You are a political fact analysis assistant. Return only valid JSON."
	temps := []float64{0.1, 0.3}
	var lastErr error
	for i, t := range temps {
		content, err := sm.callJSONLLM(ctx, sys, prompt, t, 2000)
		if err != nil {
			lastErr = fmt.Errorf("LLM call: %w", err)
			log.Warn().Err(err).Int("attempt", i+1).Float64("temperature", t).Msg("Stance LLM call failed; retrying")
			continue
		}
		resp, parseErr := parseStanceLLMResponse(content)
		if parseErr == nil {
			if i > 0 {
				log.Info().Int("attempts", i+1).Float64("recovered_at_temperature", t).Msg("Stance LLM parse succeeded after retry")
			}
			return resp, nil
		}
		lastErr = fmt.Errorf("parsing stance LLM response: %w", parseErr)
		log.Warn().Err(parseErr).Int("attempt", i+1).Float64("temperature", t).Str("llm_content_prefix", truncate(content, 200)).Msg("Stance LLM parse failed; retrying")
	}
	return nil, lastErr
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

ASSIGNMENT RULES (lean HARD toward consolidation, not fragmentation):
- A topic is a NEUTRAL SUBJECT AREA — the underlying issue, not a position on it. "Iran policy" covers BOTH support for and opposition to Iran-related actions; "politique des dépenses publiques" covers BOTH defending public services AND advocating austerity. The same topic label must work whether the person is pro, anti, mixed, or evolving on the subject.
- Assign the fact to an existing topic if it shares the SAME subject area, regardless of whether the new fact's direction agrees or disagrees with the existing topic's facts. Different events / different aspects / different positions on the same subject all belong together.
- **Polarity within a subject is CONTRADICTION MATERIAL, not a reason to fragment.** If the new fact opposes existing facts in a topic on the same subject area, assign it to that topic anyway. The internal tension is precisely what the contradiction detector exists to surface downstream. NEVER create a new topic just because the new fact's direction differs from the existing facts.
  * "promised to defend public hospitals" + later "voted for austerity cutting hospital subsidies" → SAME topic ("politique des dépenses publiques"), not two topics.
  * "promised 10%% teacher raise" + later "voted against teacher raise" → SAME topic ("politique salariale des enseignants").
  * "rebuild French artisanat" + "authorise offshoring of French factory" + "tariffs on Chinese imports" → SAME topic ("souveraineté industrielle française").
  * "protect civil liberties" + "expand surveillance cameras" + "encryption backdoors" → SAME topic ("libertés civiles et surveillance numérique").
  * "fight climate change" + "vote against carbon tax" + "delay EV mandate" → SAME topic ("politique climatique").
- Examples that should consolidate (different aspects of the same subject):
  * "sentencing in PA case" + "denial of wrongdoing in PA case" + "comments on bracelet condition" → all one "presidential candidacy and PA case" topic.
  * "Iran sanctions threats" + "Iran nuclear talks" + "Iran-Saudi attack confirmation" → all one "Iran policy" topic.
  * "EU debt criticism" + "euro inadapté" + "demanding to leave the EU" → all one "EU economic policy" topic.
- Only create a NEW topic when the fact is about a GENUINELY DIFFERENT SUBJECT AREA — not a different direction on the same subject. A "tax policy" fact and a "foreign policy" fact are different subjects. A "pro-environment" fact and an "anti-environment" fact are the SAME subject.
- When in doubt between assigning vs creating new, ALWAYS prefer assigning to the closest existing topic over creating a new one. A slightly imperfect consolidation is far better than fragmentation: the contradiction detector tolerates broad topics; it cannot recover from fragmentation.

LABEL RULES (for new_topic_label when creating a new topic):
- A LABEL IS A NEUTRAL SUBJECT, NOT A POSITION. Labels must name a noun-phrase subject area. They MUST NOT contain directional verbs, attitudinal adjectives, or polarity qualifiers. The same label must work whether the person is pro, anti, mixed, or evolving on the subject.
- FORBIDDEN label vocabulary (non-exhaustive — apply the spirit, not just the list):
  * Directional verbs / verbal nouns: "défense de", "défendre", "lutte contre", "opposition à", "soutien à", "attachement à", "engagement pour", "discipline", "protection of", "support for", "fight against", "rejet de", "promotion de"
  * Polarity prefixes: "pro-", "anti-"
  * Attitudinal adjectives in the label: "strict", "ambitieux", "sacré", "ferme", "rigoureux"
  * Stance descriptors: "posture sur X", "position concernant Y", "vision de Z"
- GOOD labels (neutral subject containers):
  * "politique des dépenses publiques" (covers BOTH "defends hospitals" AND "voted for austerity cuts")
  * "services publics et hôpitaux"
  * "politique salariale des enseignants" (covers BOTH "promised raise" AND "voted against raise")
  * "souveraineté industrielle française" (covers tariffs, offshoring, artisanat)
  * "politique climatique" (covers renewables, fossil fuels, EV mandates, carbon tax)
  * "libertés civiles et surveillance numérique"
  * "Iran policy and military operations"
  * "presidential candidacy 2027"
  * "EU economic and trade policy"
  * "affaire des assistants parlementaires"
- BAD labels (directional / position-laden) — translate to the neutral form:
  * ❌ "défense des services publics" → ✅ "services publics et hôpitaux"
  * ❌ "discipline budgétaire stricte" → ✅ "politique budgétaire de l'État"
  * ❌ "lutte contre le changement climatique" → ✅ "politique climatique"
  * ❌ "engagement pour les droits LGBT" → ✅ "droits LGBT et coparentalité"
  * ❌ "soutien à la transition écologique" → ✅ "transition écologique"
  * ❌ "opposition à la surveillance" → ✅ "surveillance et vie privée"
- Format: "{subject area}{ optional temporal scope}". Max 120 chars.
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
	prompt := fmt.Sprintf(`Generate a single GENERAL topic label for the political fact below. The label is a NEUTRAL SUBJECT AREA broad enough that 5-20 related future facts — including facts that take OPPOSITE positions on this subject — can all fit under it.

FACT:
%s

LABEL RULES:
- A LABEL IS A NEUTRAL SUBJECT, NOT A POSITION. The same label must work whether future facts are pro, anti, mixed, or evolving on the subject.
- FORBIDDEN label vocabulary (apply the spirit):
  * Directional verbs / verbal nouns: "défense de", "défendre", "lutte contre", "opposition à", "soutien à", "attachement à", "engagement pour", "discipline", "support for", "fight against", "rejet de", "promotion de"
  * Polarity prefixes: "pro-", "anti-"
  * Attitudinal adjectives: "strict", "ambitieux", "sacré", "ferme", "rigoureux"
- GOOD label examples (neutral subject containers): "Iran policy", "presidential candidacy 2027", "EU economic policy", "parliamentary assistants case", "souveraineté industrielle française", "politique climatique", "politique des dépenses publiques", "services publics et hôpitaux", "libertés civiles et surveillance numérique", "droits LGBT et coparentalité".
- BAD label examples (translate to neutral):
  * ❌ "défense des services publics" → ✅ "services publics et hôpitaux"
  * ❌ "lutte contre le changement climatique" → ✅ "politique climatique"
  * ❌ "soutien à la transition écologique" → ✅ "transition écologique"
  * ❌ "discipline budgétaire stricte" → ✅ "politique budgétaire de l'État"
- Format: "{subject area}{ optional temporal scope}". Max 120 chars.
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
- A LABEL IS A NEUTRAL SUBJECT, NOT A POSITION. The label names the subject area broadly enough that all listed facts — including any that take opposite positions on the subject — fit under it. The same label must work whether the person is pro, anti, mixed, or evolving.
- FORBIDDEN label vocabulary (apply the spirit):
  * Directional verbs / verbal nouns: "défense de", "défendre", "lutte contre", "opposition à", "soutien à", "attachement à", "engagement pour", "discipline", "support for", "fight against", "rejet de", "promotion de"
  * Polarity prefixes: "pro-", "anti-"
  * Attitudinal adjectives: "strict", "ambitieux", "sacré", "ferme", "rigoureux"
- GOOD label examples (neutral subject containers): "Iran policy and military operations", "presidential candidacy 2027", "EU economic and trade policy", "parliamentary assistants case", "souveraineté industrielle française", "politique climatique", "politique des dépenses publiques", "services publics et hôpitaux", "libertés civiles et surveillance numérique", "politique salariale des enseignants".
- BAD label examples (translate to neutral):
  * ❌ "défense des services publics" → ✅ "services publics et hôpitaux"
  * ❌ "lutte contre le changement climatique" → ✅ "politique climatique"
  * ❌ "discipline budgétaire stricte" → ✅ "politique budgétaire de l'État"
  * ❌ "soutien à la transition écologique" → ✅ "transition écologique"
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
// proposes, returns a unified summary + label. The caller is either Phase-A
// average-linkage clustering (embedding-similar stances) or — under the
// post-Framing-A regime — the all-stances-per-person path that hands the LLM
// every stance the person has. Either way the LLM's job is to identify subsets
// covering the SAME NEUTRAL SUBJECT AREA (regardless of polarity within) and
// write the unified label + summary.
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

	prompt := fmt.Sprintf(`You are deciding which subsets of the listed stance documents about %s should be merged because they cover the SAME NEUTRAL SUBJECT AREA. A stance is defined as a neutral subject container — both supportive and opposing facts on that subject belong inside it.

MERGE RULES:

**THINK IN TEXTBOOK SUBJECT CATEGORIES.** When deciding whether two stances cover the same subject, ask: "would a political science textbook, a national administration's policy index, or a journalist's beat description put these facts under the same heading?" If yes → MERGE. The canonical taxonomy (use these as the merge target when applicable):
- DOMESTIC POLICY: politique économique, politique fiscale, politique sociale, politique de l'emploi, politique du logement, politique des retraites, politique de santé, politique de l'éducation, politique culturelle.
- ECONOMIC & INDUSTRIAL: politique industrielle, politique commerciale, souveraineté économique, politique monétaire, politique de la concurrence, politique agricole.
- ENVIRONMENTAL & ENERGY: politique climatique, politique énergétique, transition écologique.
- SECURITY & JUSTICE: politique de sécurité intérieure, politique pénale, libertés civiles et surveillance, immigration et asile.
- FOREIGN POLICY: politique étrangère, politique européenne, défense nationale, relations bilatérales (e.g. Iran policy, Russia policy).
- INSTITUTIONS: réforme institutionnelle, décentralisation, fonction publique, laïcité.
- PERSONAL / LEGAL: a specific legal case ("affaire des assistants parlementaires"), a specific candidacy ("présidentielle 2027").

Fine-grained stances usually roll up into ONE of these textbook headings. If three stances each cover an aspect that a public administration would file under "politique industrielle française", they MUST merge into "politique industrielle française" (or a slightly more specific label like "souveraineté industrielle française"), regardless of which side of the policy they advocate.

- **MERGE WHEN: stances cover the same textbook subject area, REGARDLESS OF POLARITY OR DIRECTION.** A stance whose facts express support for X and a stance whose facts express opposition to X cover the SAME subject (X) and MUST merge. The whole point of consolidation is to put all of the person's positions on one subject in one place so the contradiction detector can surface internal tension.

- Worked examples that MUST merge (textbook-category framing):
  * "artisanat et pouvoir d'achat" + "politique industrielle et délocalisation" + "politique commerciale et douanière" → unified label "souveraineté industrielle française" or "politique industrielle et commerciale" (all three fit under industrial/trade policy in any textbook)
  * "défense des services publics" + "discipline budgétaire" + "politique salariale des enseignants" → unified label "politique des dépenses publiques" (all three are aspects of state-spending policy — even though one is teachers, one is hospitals, one is austerity, they all answer "how much does the state spend and on what")
  * "transition écologique" + "taxe carbone" + "politique énergétique nucléaire" → "politique énergétique et climatique"
  * "réforme des retraites" + "politique salariale des enseignants" + "fonction publique" → "politique sociale et fonction publique" (broader textbook category that covers all three)
  * "politique étrangère et coopération de défense" + "diplomatie internationale" + "modernisation militaire" → "politique étrangère et de défense"
  * "libertés civiles et surveillance" + "encryption et messageries chiffrées" + "vidéosurveillance algorithmique" → "libertés civiles et surveillance numérique"
  * "droits des couples homoparentaux" + "mariage pour tous" + "lutte contre les discriminations LGBT" → "droits LGBT" or "droits civils et familiaux"
  * Two labels that share a key phrase (e.g. both contain "transition écologique", "services publics", "industrie", "défense", "retraites") almost always belong together — merge unless the summaries make clear they're genuinely different subjects.

- DO NOT MERGE when stances are about GENUINELY DIFFERENT SUBJECT AREAS at the textbook level:
  * "politique étrangère" vs "politique fiscale" → different chapters of the textbook, keep separate
  * "politique étrangère et de défense" vs "politique de sécurité intérieure" → different domains (external vs internal) even though both touch "security", keep separate
  * Two unrelated specific legal cases involving different events → keep separate (e.g. "affaire des assistants parlementaires" vs "affaire Karachi" — same person but two distinct case files)
  * Two elections in different jurisdictions / years that don't share a campaign theme → keep separate
  * A specific case ("affaire des assistants parlementaires") vs a general policy stance ("politique judiciaire") → keep separate (case-level vs policy-level)

- TEMPORAL CONTEXT alone is NOT a reason to keep separate if both bear on the same recurring subject (e.g. "budget 2024" vs "budget 2025" → MERGE under "politique budgétaire de l'État"). Distinct legal cases tied to different events are a separate-subject scenario; recurring policy themes are the same subject.

- When in doubt: PREFER to merge AT THE TEXTBOOK LEVEL. Fragmentation hurts contradiction detection because contradictions only fire within a single stance. Over-merging at the textbook level tolerates broad subjects (the inline detector can still distinguish facts within); under-merging silently loses cross-fragment contradictions forever.

- Singletons (single-stance "merges") are NOT allowed — every merge group must contain at least 2 stance IDs.

UNIFIED LABEL RULES (for the ` + "`label`" + ` field of each merge):
- The unified label MUST be a NEUTRAL SUBJECT, not a position. The same label must work for both pro and anti facts in the merged stance.
- FORBIDDEN label vocabulary: directional verbs ("défense de", "lutte contre", "opposition à", "soutien à", "engagement pour", "discipline"), polarity prefixes ("pro-", "anti-"), attitudinal adjectives ("strict", "sacré", "ferme").
- GOOD unified labels: "politique des dépenses publiques", "politique climatique", "services publics et hôpitaux", "souveraineté industrielle française", "Iran policy", "EU economic policy".
- BAD unified labels: ❌ "défense de la transition écologique" → ✅ "politique climatique". ❌ "engagement pour les services publics" → ✅ "services publics et hôpitaux".
- Length: 4-12 words. Max 120 chars.

UNIFIED SUMMARY RULES (for the ` + "`summary`" + ` field):
- 1-3 sentences describing what the person has said / done across all merged stances on this subject.
- If merged stances express OPPOSING positions on the subject, the summary MUST PRESERVE THE TENSION — name both directions explicitly. Do not smooth opposing positions into a single narrative. Example: "X has promised to defend public hospitals (services publics stance) while simultaneously voting for austerity budgets that cut hospital subsidies (discipline budgétaire stance)."

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
