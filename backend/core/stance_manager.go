package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
)

// StanceStorer defines the storage interface for stance operations.
type StanceStorer interface {
	InsertStanceDocument(ctx context.Context, doc StanceDocument) (int64, error)
	UpdateStanceDocument(ctx context.Context, id int64, summary, summaryEn string, embedding []float32, keyFactIDs []int64) error
	// UpdateStanceMetadata stores a refreshed label + summary and advances the
	// stance's summarized_at cursor to the newest membership among
	// consumedFactIDs (the facts the refresh actually absorbed), so a fact
	// added while the LLM was running stays pending.
	UpdateStanceMetadata(ctx context.Context, id int64, label, summary string, consumedFactIDs []int64) error
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error)
	GetStanceDocument(ctx context.Context, id int64) (StanceDocument, error)
	// ShortlistStancesByNearestFact ranks the person's stances by their
	// nearest member fact and returns the topK with up to factsPerStance
	// member facts each as evidence. excludeFactID (0 = none) hides one fact
	// from the evidence: a fact re-checked against its own stance must not
	// be its own nearest neighbour.
	ShortlistStancesByNearestFact(ctx context.Context, personID int64, factEmbedding []float32, topK, factsPerStance int, excludeFactID int64) ([]StanceCandidate, error)
	// StanceCandidatesByIDs returns the given stances as candidates in the same
	// shape as ShortlistStancesByNearestFact, ordered by nearest member fact.
	StanceCandidatesByIDs(ctx context.Context, personID int64, factEmbedding []float32, stanceIDs []int64, factsPerStance int, excludeFactID int64) ([]StanceCandidate, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	// GetClosestFactsForStance returns the limit facts of the stance nearest to
	// embedding, nearest first, without the grouped duplicates.
	GetClosestFactsForStance(ctx context.Context, stanceID int64, embedding []float32, limit int) ([]StanceFactInfo, error)
	// GetUnsummarizedFactsForStance returns the facts whose membership was added
	// after the stance's last metadata refresh (all facts if never refreshed),
	// oldest membership first.
	GetUnsummarizedFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	// GetStanceMetadata returns the stance's current native label and summary.
	GetStanceMetadata(ctx context.Context, stanceID int64) (label, summary string, err error)
	// GetStanceTensions returns, per fact of the stance, the recorded
	// contradictions it is part of with other facts of the same stance.
	GetStanceTensions(ctx context.Context, stanceID int64) (map[int64][]StanceTension, error)
	// RecordContradictionImmediate records a contradiction between two facts
	// of the person in its own transaction and returns its id and whether the
	// bundler still has to resolve its stance.
	RecordContradictionImmediate(ctx context.Context, personID int64, factAID int64, factBID int64, reason, reasonEn string) (contradictionID int64, needsBundler bool, err error)
	// EnqueueFactPair asks the contradiction detector to judge a pair.
	EnqueueFactPair(ctx context.Context, personID, factA, factB int64, source string) error
	// RecordJudgedPairs records the verdict of the stance analysis on the
	// pairs of a new fact and the key facts it was compared with, under
	// judgeHash (StanceJudgeHash), so the detection does not judge them again.
	RecordJudgedPairs(ctx context.Context, personID int64, judgeHash string, pairs []JudgedPair) error
}

// JudgedPair is the verdict of a judge on a pair of facts of one person.
type JudgedPair struct {
	FactAID, FactBID int64
	Contradiction    bool
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
	LastUpdatedAt    time.Time
	CreatedAt        time.Time
}

// StanceCandidate is one shortlisted stance for topic assignment, ranked by its
// nearest member fact to the incoming fact's embedding and carrying that
// stance's closest member fact texts as concrete evidence for the LLM.
type StanceCandidate struct {
	ID          int64
	Label       string
	Summary     string
	FactCount   int
	MemberFacts []string
	NearestDist float64
}

// StanceFactInfo represents a fact within a stance.
type StanceFactInfo struct {
	ID                 int64
	NormalizedFactText string
	EmbeddingText      string
	CanonicalVerb      string
	Lang               string // BCP-47 source language ("fr", "en", ...); always set on new inserts.
	// Date is the day of the fact (its earliest source), nil when unknown.
	Date *time.Time
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

// The verdicts of a stance decision.
const (
	VerdictNewStance     = "NEW_STANCE"
	VerdictDuplicate     = "DUPLICATE"
	VerdictNewInfo       = "NEW_INFO"
	VerdictContradiction = "CONTRADICTION"
)

// StanceDecision is where a fact goes and what it does to its stance, decided
// before anything is written. ApplyStanceDecision writes it.
//   - VerdictNewStance: a new stance labelled NewLabel (NewLabelEn optional).
//   - VerdictDuplicate, VerdictNewInfo: the fact joins stance StanceID.
//   - VerdictContradiction: the fact joins stance StanceID, and each entry of
//     Contradictions is recorded. The entries must be verified already.
type StanceDecision struct {
	StanceID       int64
	NewLabel       string
	NewLabelEn     string
	Verdict        string
	Contradictions []StanceContradictionEntry
	Reasoning      string
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
	AssignedIndex   int            `json:"assigned_topic_index"` // 1-indexed, 0 means new topic
	NewTopicLabel   flexibleString `json:"new_topic_label"`      // only set when AssignedIndex == 0
	NewTopicLabelEn flexibleString `json:"new_topic_label_en"`   // English translation; empty when new_topic_label is already English
}

// topicAssignmentDecision is the resolved result of assignTopic — the LLM's
// 1-based candidate index has already been mapped to a concrete stance ID (or
// IsNew when the fact belongs to a genuinely new topic).
type topicAssignmentDecision struct {
	AssignedID int64  // stance ID the fact was routed to; meaningful only when IsNew is false
	IsNew      bool   // true when no existing candidate fit and a new stance must be created
	NewLabel   string // proposed label for the new stance (IsNew only)
	NewLabelEn string // English translation of NewLabel (IsNew only)
	Reasoning  string // LLM's brief explanation of its choice
	// ViaLabelPass is true when the assigned stance was outside the embedding
	// shortlist and only the label pass offered it.
	ViaLabelPass bool
}

// generalLabelResponse is the response from SubjectLabel.
type generalLabelResponse struct {
	Label flexibleString `json:"label"`
}

// stanceMetadataResponse is the response from refreshStanceMetadata — a fresh
// general topic label and a 2-4 sentence summary regenerated from the FULL
// fact list of the stance.
type stanceMetadataResponse struct {
	Label   flexibleString `json:"label"`
	Summary string         `json:"summary"`
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

// keyFactBatchSize is a HARD BACKSTOP on the number of key facts per analyze
// call. The real limit is the token budget (see chunkKeyFactsByBudget) — this
// only guards against a pathological case where many tiny facts would otherwise
// pack into one oversized batch. Stances larger than a batch are processed
// across multiple sequential analyze calls; contradictions are unioned.
const keyFactBatchSize = 40

// stanceCompareFacts is how many facts of its stance a new fact is compared
// with: its nearest ones, one analyze call on Claude.
const stanceCompareFacts = keyFactBatchSize

// stanceContextTokens is the context of the endpoint that serves task in
// the active routing (14K on the local box, 200K on Claude): the budget of
// the prompt is sized on it, not on a constant of one model (the 12K of the
// first local model left no room once the rules grew).
func stanceContextTokens(task llm.Task) int { return llm.ContextTokens(task) }

// Token budget for the stance-analyze LLM call. The analyze prompt is dominated
// by the shared promptrules.ContradictionRules body (~6.3K tokens) plus the
// template prose; with the model's 12K context that leaves little room, so the
// key-fact batch is sized dynamically against the MEASURED base (see
// estimateStanceBaseTokens). Measuring the rules directly means this can't go
// stale when they grow — the regression that overflowed the context and rolled
// back facts in prod (the old code reserved 4K output against an 8K base = 12K,
// leaving zero room for facts).
const (
	stanceAnalyzeOutputTokens = 2048 // was 4000; budget-batching bounds entries/output per call so this is ample
	stanceContextSafetyTokens = 600  // absorbs token-estimate error
	stanceCharsPerToken       = 4    // measured ~4.1 on the French analyze prompt; 4 is slightly conservative
	// stanceTemplateProseTokens is the fixed analyze-template prose AROUND the
	// shared rules (which are measured directly in estimateStanceBaseTokens).
	// Bump only if you substantially edit the analyze template in callLLM.
	stanceTemplateProseTokens = 2200
	// stanceMinFactsBudget keeps batching making progress even if the base prompt
	// is near the ceiling (signals the prompt needs trimming — logged at WARN).
	stanceMinFactsBudget = 200
)

// StanceManager handles the core stance document logic: matching new facts to existing
// stances, creating new stances, and detecting contradictions via LLM analysis.
type StanceManager struct {
	storer StanceStorer
	client *llm.Client
	// restate keeps a contradiction counted twice out (RestatesExisting);
	// nil: no check.
	restate *RestatementJudge
}

// NewStanceManagerFromEnv creates a new StanceManager using OPEN_AI_* environment variables.
// Returns an error if required env vars (OPEN_AI_KEY, OPEN_AI_API) are missing.
func NewStanceManagerFromEnv(storer StanceStorer) (*StanceManager, error) {
	client, err := llm.NewClientFromEnv("stance-manager")
	if err != nil {
		return nil, err
	}
	restate, err := NewRestatementJudgeFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("stance manager: no restatement judge; a contradiction counted twice is recorded twice")
		restate = nil
	}
	return &StanceManager{storer: storer, client: client, restate: restate}, nil
}

// ErrStanceRefreshPending marks a ProcessNewFact error returned after the fact
// was committed to its stance (membership, contradictions, centroid): only the
// label + summary refresh failed, because the LLM was unavailable. The fact
// stays pending for the stance's next refresh (summarized_at), so the caller
// must keep the fact, not roll it back.
var ErrStanceRefreshPending = errors.New("fact linked to its stance; label and summary refresh pending")

// ProcessNewFact analyzes a new fact against existing stances for the given person.
// It uses LLM-based topic assignment to find the right stance (or to generate a
// general topic label when no existing stance fits), then analyzes the new fact
// pair-wise against all key facts in the matched stance. After a non-duplicate
// verdict, the new fact (and its verified tensions) is folded into the stance's
// label + summary (see refreshAfterVerdict). An error wrapping
// ErrStanceRefreshPending means the fact was routed and only that refresh
// failed.
func (sm *StanceManager) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string, date *time.Time) (*StanceProcessResult, error) {
	decision, matched, err := sm.assignStance(ctx, personID, factID, factText, embedding)
	if err != nil {
		return nil, err
	}
	if decision.Verdict == VerdictNewStance {
		return sm.ApplyStanceDecision(ctx, personID, factID, factText, embedding, decision)
	}

	// The new fact is compared with the facts of the stance closest to it, not
	// with all of them: stances reach 1,100 facts, and comparing with all of
	// them took 47% of the seats' budget (2026-11-05). The maintenance pass's
	// detection judges the nearest pairs of the person across the stances.
	keyFacts, err := sm.storer.GetClosestFactsForStance(ctx, matched.ID, embedding, stanceCompareFacts)
	if err != nil {
		return nil, fmt.Errorf("getting facts for stance %d: %w", matched.ID, err)
	}

	// Analyze the new fact against those facts. When they do not fit in one
	// prompt, batch the call and union the contradictions across batches. The
	// fact joins the stance only once the
	// analysis and its verification are done (handleVerdict): a failed call
	// leaves nothing written, so the (person, fact) pair stays pending.
	agg, err := sm.analyzeAcrossBatches(ctx, matched.Summary, keyFacts, factText, date)
	if err != nil {
		return nil, fmt.Errorf("LLM stance analysis: %w", err)
	}

	res, err := sm.handleVerdict(ctx, matched, personID, factID, factText, date, keyFacts, embedding, agg)
	if err != nil {
		return nil, err
	}
	sm.recordJudgedPairs(ctx, personID, factID, keyFacts, res)
	return res, nil
}

// recordJudgedPairs records the verdict of the analysis on the pairs of the
// new fact and the key facts it was compared with: a contradiction for the
// pairs handleVerdict kept, none for the others. The maintenance pass's
// detection then skips these pairs (fact_pair_checks): over a week of
// November 2026 it judged 81,000 pairs for 61 contradictions, and the closest
// facts of a person are mostly the key facts of its stance, judged here
// already. A failure to record is logged: the fact is in, the pairs are only
// judged again later.
func (sm *StanceManager) recordJudgedPairs(ctx context.Context, personID, factID int64, keyFacts []StanceFactInfo, res *StanceProcessResult) {
	contradicted := map[int64]bool{}
	if res != nil {
		for _, c := range res.Contradictions {
			contradicted[c.KeyFactID] = true
		}
	}
	pairs := make([]JudgedPair, 0, len(keyFacts))
	for _, f := range keyFacts {
		if f.ID == factID || f.ID <= 0 {
			continue
		}
		pairs = append(pairs, JudgedPair{FactAID: factID, FactBID: f.ID, Contradiction: contradicted[f.ID]})
	}
	if len(pairs) == 0 {
		return
	}
	if err := sm.storer.RecordJudgedPairs(ctx, personID, StanceJudgeHash(), pairs); err != nil {
		log.Warn().Err(err).Int64("fact_id", factID).Int("pairs", len(pairs)).Msg("stance analysis: the judged pairs were not recorded")
	}
}

// AssignStance decides the stance of a fact of the person without the
// analysis against the facts of that stance: Verdict is VerdictNewStance
// (with a label) or VerdictNewInfo (with StanceID). It writes nothing; give
// the decision to ApplyStanceDecision. A caller uses it when its facts need no
// stance analysis, for example a source whose pairs the detection judges.
func (sm *StanceManager) AssignStance(ctx context.Context, personID, factID int64, factText string, embedding []float32) (StanceDecision, error) {
	decision, _, err := sm.assignStance(ctx, personID, factID, factText, embedding)
	return decision, err
}

// assignStance routes the fact to one of the person's stances or to a new
// stance. For an existing stance it also returns that stance. factID is only
// logged.
func (sm *StanceManager) assignStance(ctx context.Context, personID, factID int64, factText string, embedding []float32) (StanceDecision, *StanceDocument, error) {
	stances, err := sm.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return StanceDecision{}, nil, fmt.Errorf("getting stances for person: %w", err)
	}

	// Always go through assignTopic — even when no stances exist, the LLM emits a
	// general topic label per the LABEL RULES instead of falling back to fact text.
	// When the fact has an embedding, assignTopic shortlists candidate stances by
	// their NEAREST member fact and shows the LLM that concrete evidence.
	assignment, err := sm.assignTopic(ctx, personID, stances, factText, embedding, assignExclude{})
	if err != nil {
		return StanceDecision{}, nil, fmt.Errorf("topic assignment: %w", err)
	}

	// New topic: create a new stance with the LLM-generated general label
	if assignment.IsNew {
		metrics.RecordDecision(metrics.StageStanceAssign, metrics.OutcomeStanceNew)
		label := assignment.NewLabel
		if label == "" {
			// Defensive: assignTopic didn't emit a usable label. Ask explicitly via
			// SubjectLabel so we never store fact text as a stance label.
			label, err = sm.SubjectLabel(ctx, factText)
			if err != nil {
				return StanceDecision{}, nil, fmt.Errorf("generating fallback general label: %w", err)
			}
		}
		log.Info().
			Str("new_topic", label).
			Int64("fact_id", factID).
			Str("reasoning", assignment.Reasoning).
			Msg("LLM assigned fact to new topic")
		return StanceDecision{Verdict: VerdictNewStance, NewLabel: label, NewLabelEn: assignment.NewLabelEn, Reasoning: assignment.Reasoning}, nil, nil
	}

	// Find the matched stance by the resolved stance ID.
	var matched *StanceDocument
	for i := range stances {
		if stances[i].ID == assignment.AssignedID {
			matched = &stances[i]
			break
		}
	}
	if matched == nil {
		log.Warn().
			Int64("assigned_stance_id", assignment.AssignedID).
			Int("total_stances", len(stances)).
			Msg("LLM-assigned stance ID not found among person's stances, creating new stance")
		label, err := sm.SubjectLabel(ctx, factText)
		if err != nil {
			return StanceDecision{}, nil, fmt.Errorf("generating fallback general label: %w", err)
		}
		return StanceDecision{Verdict: VerdictNewStance, NewLabel: label}, nil, nil
	}

	metrics.RecordDecision(metrics.StageStanceAssign, metrics.OutcomeStanceJoined)
	if assignment.ViaLabelPass {
		// The embedding shortlist did not contain this stance; only the label
		// pass surfaced it. Counts how much recall that pass actually buys.
		metrics.RecordDecision(metrics.StageStanceAssign, metrics.OutcomeStanceLabelRescue)
	}
	log.Info().
		Int64("stance_id", matched.ID).
		Str("topic", matched.ClusterLabel).
		Int64("fact_id", factID).
		Str("reasoning", assignment.Reasoning).
		Bool("via_label_pass", assignment.ViaLabelPass).
		Msg("LLM assigned fact to existing topic")
	return StanceDecision{StanceID: matched.ID, Verdict: VerdictNewInfo, Reasoning: assignment.Reasoning}, matched, nil
}

// ApplyStanceDecision writes decision d for the fact factID of the person: the
// stance membership, the fact count, centroid and key facts of the stance, and
// the contradictions of d. It makes no LLM call. It does not refresh the label
// and summary of the stance: the new membership stays pending (summarized_at)
// for RefreshStanceMetadata or RefreshPendingStances.
func (sm *StanceManager) ApplyStanceDecision(ctx context.Context, personID, factID int64, factText string, embedding []float32, d StanceDecision) (*StanceProcessResult, error) {
	if d.Verdict == VerdictNewStance {
		if len(d.Contradictions) > 0 {
			return nil, fmt.Errorf("a %s decision for fact %d has %d contradictions", d.Verdict, factID, len(d.Contradictions))
		}
		return sm.createNewStance(ctx, personID, factID, factText, embedding, d.NewLabel, d.NewLabelEn)
	}
	matched, err := sm.storer.GetStanceDocument(ctx, d.StanceID)
	if err != nil {
		return nil, err
	}
	if matched.PersonID != personID {
		return nil, fmt.Errorf("stance %d belongs to person %d, not to person %d", matched.ID, matched.PersonID, personID)
	}
	return sm.applyToStance(ctx, &matched, personID, factID, embedding, d)
}

// applyToStance writes decision d for a fact that joins the existing stance
// matched. A duplicate only joins; new info and a contradiction also update
// the counters, centroid and key facts of the stance.
func (sm *StanceManager) applyToStance(ctx context.Context, matched *StanceDocument, personID, factID int64, embedding []float32, d StanceDecision) (*StanceProcessResult, error) {
	switch d.Verdict {
	case VerdictDuplicate, VerdictNewInfo:
		if len(d.Contradictions) > 0 {
			return nil, fmt.Errorf("a %s decision for fact %d has %d contradictions", d.Verdict, factID, len(d.Contradictions))
		}
	case VerdictContradiction:
		if len(d.Contradictions) == 0 {
			return nil, fmt.Errorf("a %s decision for fact %d has no contradiction", d.Verdict, factID)
		}
	default:
		return nil, fmt.Errorf("unexpected stance verdict: %q", d.Verdict)
	}

	if err := sm.storer.AddFactToStance(ctx, matched.ID, factID); err != nil {
		return nil, fmt.Errorf("adding fact to stance %d: %w", matched.ID, err)
	}
	result := &StanceProcessResult{Verdict: d.Verdict, StanceID: matched.ID, Reasoning: d.Reasoning, Contradictions: d.Contradictions}
	if d.Verdict == VerdictDuplicate {
		return result, nil
	}
	if d.Verdict == VerdictContradiction {
		recorded := sm.recordStanceContradictions(ctx, personID, factID, matched.ID, d.Contradictions)
		log.Warn().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("contradictions_recorded", recorded).
			Msg("Recorded contradictions in stance")
	}
	if err := sm.applyStanceUpdate(ctx, matched, factID, embedding); err != nil {
		return nil, err
	}
	return result, nil
}

// keyTextByID builds a lookup of fact ID → normalized fact text from a slice of
// StanceFactInfo, used by verifyEntries to fetch the text of each candidate
// key fact without an extra DB round-trip.
func keyTextByID(facts []StanceFactInfo) map[int64]string {
	m := make(map[int64]string, len(facts))
	for _, f := range facts {
		m[f.ID] = f.NormalizedFactText
	}
	return m
}

// aggregatedAnalysis is the cross-batch result of analyzeAcrossBatches.
type aggregatedAnalysis struct {
	Reasoning      string                     // concatenation of per-batch reasoning lines
	PrimaryVerdict string                     // DUPLICATE | NEW_INFO | CONTRADICTION
	DuplicateOf    int64                      // first non-zero duplicate_of_key_fact_id seen
	Contradictions []StanceContradictionEntry // deduped by key_fact_id
}

// AnalyzeVotes is the majority-vote count for the stance analyze judge
// (judge #1, the only edge creator) on an endpoint that votes (llm.Votes).
var AnalyzeVotes = 3

// analyzeVoteKey reduces an analyze response to what must agree across votes:
// the verdict, the duplicate target and the exact contradiction edge set.
// Reasoning text may differ between agreeing votes.
func analyzeVoteKey(r *stanceLLMResponse) string {
	ids := make([]string, 0, len(r.Contradictions))
	for _, c := range r.Contradictions {
		ids = append(ids, strconv.FormatInt(c.KeyFactID, 10))
	}
	sort.Strings(ids)
	return r.PrimaryVerdict + "|" + strconv.FormatInt(r.DuplicateOf, 10) + "|" + strings.Join(ids, ",")
}

// analyzeAcrossBatches splits keyFacts into batches (chunkKeyFactsByBudget)
// and calls callLLM once per batch with the same summary + new fact. The
// batches run as many at once as the endpoint serves (llm.ForEach); after the
// first failed batch no other starts, and its error is returned. The answers are aggregated in
// batch order, so the result does not depend on the order of completion.
// Across all batches the contradictions list is unioned (deduped by
// KeyFactID). The primary verdict is CONTRADICTION if any batch returned
// contradictions, else DUPLICATE if any batch returned DUPLICATE, else
// NEW_INFO.
func (sm *StanceManager) analyzeAcrossBatches(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string, newFactDate *time.Time) (*aggregatedAnalysis, error) {
	batches := chunkKeyFactsByBudget(stanceSummary, newFactText, keyFacts)
	if len(batches) == 0 {
		// No key facts at all — degenerate but possible. Treat the new fact as
		// NEW_INFO so the stance gets the new fact attached without a contradiction
		// claim. The refresh call will produce a label/summary from the now-1 fact.
		return &aggregatedAnalysis{PrimaryVerdict: "NEW_INFO"}, nil
	}

	resps := make([]*stanceLLMResponse, len(batches))
	idxs := make([]int, len(batches))
	for i := range idxs {
		idxs[i] = i
	}
	err := llm.ForEach(ctx, idxs, func(ctx context.Context, batchIdx int) error {
		// On the local GPU box the verdict is a sample and this judge CREATES
		// contradiction edges: vote per batch, keyed on (verdict, duplicate, edge
		// set), as the verifier and the auditor do (mirror-hc4: D_denial flaked
		// ~25% single-sample). On Claude llm.Votes makes it one call.
		resp, err := llm.MajorityVote(llm.Votes(llm.TaskStanceAnalyze, AnalyzeVotes),
			func() (*stanceLLMResponse, error) {
				return sm.callLLM(ctx, stanceSummary, batches[batchIdx], newFactText, newFactDate)
			},
			analyzeVoteKey,
		)
		if err != nil {
			return fmt.Errorf("analyze batch %d/%d: %w", batchIdx+1, len(batches), err)
		}
		resps[batchIdx] = resp
		return nil
	})
	if err != nil {
		return nil, err
	}

	agg := &aggregatedAnalysis{}
	seen := make(map[int64]bool)
	var hasDuplicate, hasNewInfo bool
	var reasoningParts []string

	for _, resp := range resps {
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

// estimateStanceTokens is a cheap, deliberately conservative token estimate
// (bytes ÷ stanceCharsPerToken). Byte length over-counts multibyte French text
// slightly, which biases batches smaller — the safe direction.
func estimateStanceTokens(s string) int {
	return len(s) / stanceCharsPerToken
}

// formatKeyFactLine renders one key fact as it appears in the analyze prompt.
// Shared by the prompt builder and the budget estimator so the two never drift.
func formatKeyFactLine(f StanceFactInfo) string {
	return fmt.Sprintf("- [ID %d] (%s) %s%s\n", f.ID, f.CanonicalVerb, datedPrefix(f.Date), f.NormalizedFactText)
}

// datedPrefix is the date a judge sees before a fact's text: "(dated
// YYYY-MM-DD) " when known, "(undated) " else. The rules reason on dates (a
// reversal needs both, a yearly text a year apart is two texts); until
// 2026-10-27 the stance judges saw none (the Le Pen review: two budgets a
// year apart judged as one text).
func datedPrefix(t *time.Time) string {
	d := promptrules.DateOf(t)
	if d == "" {
		return "(undated) "
	}
	return "(dated " + d + ") "
}

// keyFactByID indexes the key facts of a stance by id.
func keyFactByID(facts []StanceFactInfo) map[int64]StanceFactInfo {
	m := make(map[int64]StanceFactInfo, len(facts))
	for _, f := range facts {
		m[f.ID] = f
	}
	return m
}

// statementOf is the fact as the verifier judges it: its text and its date.
func statementOf(f StanceFactInfo) promptrules.Statement {
	return promptrules.Statement{Text: f.NormalizedFactText, Date: promptrules.DateOf(f.Date)}
}

// estimateStanceBaseTokens estimates the analyze prompt's fixed cost EXCLUDING
// key facts: the shared ContradictionRules (measured directly so rule growth
// can't silently overflow the context), the template prose, and the per-call
// summary + new fact.
func estimateStanceBaseTokens(stanceSummary, newFactText string) int {
	return stanceTemplateProseTokens +
		estimateStanceTokens(promptrules.ContradictionRules) +
		estimateStanceTokens(stanceSummary) +
		estimateStanceTokens(newFactText)
}

// chunkKeyFactsByBudget batches key facts so each analyze call fits the model's
// context: base prompt + facts + output reservation + safety ≤ stanceContextTokens().
// Falls back to a minimal budget (with a WARN) if the base prompt alone is near
// the ceiling — that signals the analyze template itself needs trimming. Never
// emits an empty batch, so a single oversized fact is still attempted alone.
func chunkKeyFactsByBudget(stanceSummary, newFactText string, facts []StanceFactInfo) [][]StanceFactInfo {
	if len(facts) == 0 {
		return nil
	}
	base := estimateStanceBaseTokens(stanceSummary, newFactText)
	factsBudget := stanceContextTokens(llm.TaskStanceAnalyze) - stanceAnalyzeOutputTokens - stanceContextSafetyTokens - base
	if factsBudget < stanceMinFactsBudget {
		log.Warn().
			Int("base_tokens", base).
			Int("facts_budget", factsBudget).
			Msg("Stance analyze base prompt near context limit; using minimal fact batches (consider trimming the prompt)")
		factsBudget = stanceMinFactsBudget
	}

	var batches [][]StanceFactInfo
	var cur []StanceFactInfo
	curTokens := 0
	for _, f := range facts {
		ft := estimateStanceTokens(formatKeyFactLine(f))
		if len(cur) > 0 && (curTokens+ft > factsBudget || len(cur) >= keyFactBatchSize) {
			batches = append(batches, cur)
			cur = nil
			curTokens = 0
		}
		cur = append(cur, f)
		curTokens += ft
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// createNewStance creates a new stance document for a fact. The label MUST be
// non-empty — callers route through assignTopic / SubjectLabel to obtain a
// general topic label. If the supplied label is empty we error rather than
// falling back to fact text (which historically produced quoted-statement
// labels that fragmented stances).
func (sm *StanceManager) createNewStance(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, label, labelEn string) (*StanceProcessResult, error) {
	label = strings.TrimSpace(label)
	labelEn = strings.TrimSpace(labelEn)
	if label == "" {
		return nil, fmt.Errorf("new stance for fact %d has no label", factID)
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
		// Summary is the first fact's text: a correct seed for the fold that runs
		// when the second fact joins (refreshAfterVerdict). SummaryEn is left to
		// the translation phase; until then the frontend falls back to native.
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
		Verdict:  VerdictNewStance,
		StanceID: stanceID,
	}, nil
}

// handleVerdict processes the aggregated analyze result: it verifies the
// flagged contradictions, writes the decision (applyToStance), and triggers a
// label+summary refresh for non-duplicate verdicts. Nothing is written before
// the verification succeeded, so a failed verifier call leaves the fact out
// of the stance, still pending.
func (sm *StanceManager) handleVerdict(ctx context.Context, matched *StanceDocument, personID int64, factID int64, newFactText string, newFactDate *time.Time, keyFacts []StanceFactInfo, embedding []float32, agg *aggregatedAnalysis) (*StanceProcessResult, error) {
	decision := StanceDecision{StanceID: matched.ID, Verdict: agg.PrimaryVerdict, Reasoning: agg.Reasoning}
	switch agg.PrimaryVerdict {
	case VerdictDuplicate, VerdictNewInfo:
	case VerdictContradiction:
		// Adversarial verification pass: survivors only — refuted pairs are
		// never recorded, so they never dent the honesty score. If every pair
		// is refuted the fact is just new info for the stance.
		verified, err := sm.verifyEntries(ctx, personID, factID, promptrules.Statement{Text: newFactText, Date: promptrules.DateOf(newFactDate)}, keyFactByID(keyFacts), agg.Contradictions)
		if err != nil {
			return nil, err
		}
		if len(verified) > 0 {
			decision.Contradictions = verified
		} else {
			decision.Verdict = VerdictNewInfo
		}
	default:
		return nil, fmt.Errorf("unexpected LLM verdict: %q", agg.PrimaryVerdict)
	}

	result, err := sm.applyToStance(ctx, matched, personID, factID, embedding, decision)
	if err != nil {
		return nil, err
	}

	switch agg.PrimaryVerdict {
	case VerdictDuplicate:
		metrics.RecordDecision(metrics.StageStanceAnalyze, metrics.OutcomeAnalyzeDuplicate)
		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int64("duplicate_of", agg.DuplicateOf).
			Str("reasoning", agg.Reasoning).
			Msg("Fact is duplicate of existing stance")

	case VerdictNewInfo:
		metrics.RecordDecision(metrics.StageStanceAnalyze, metrics.OutcomeAnalyzeNewInfo)
		log.Info().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("new_fact_count", matched.FactCount+1).
			Msg("Added new info to existing stance")

	case VerdictContradiction:
		if result.Verdict == VerdictContradiction {
			metrics.RecordDecision(metrics.StageStanceAnalyze, metrics.OutcomeAnalyzeContradiction)
		}
		log.Warn().
			Int64("stance_id", matched.ID).
			Int64("fact_id", factID).
			Int("contradictions_flagged", len(agg.Contradictions)).
			Int("contradictions_verified", len(result.Contradictions)).
			Str("reasoning", agg.Reasoning).
			Msg("Contradictions detected in stance")
	}

	// A contradiction changes what the stance says: fold the new fact and the
	// tensions the analysis found into the label + summary now. A fact that
	// only adds information waits for the maintenance pass, which folds every
	// unsummarized fact of a stance in one call (RefreshPendingStances,
	// summarized_at): refreshing at each insertion made about one label call
	// per new fact (9,700 on 2026-11-06), the pass makes one per stance
	// touched. Failures here do not fail the verdict (the stance is still
	// correct, just using its prior label/summary until the next refresh picks
	// the fact up via summarized_at); an unavailable LLM is returned, marked
	// ErrStanceRefreshPending, so the run stops.
	if result.Verdict == VerdictContradiction && !refreshDeferred(ctx) {
		if err := sm.refreshAfterVerdict(ctx, matched, factID, result.Contradictions, keyTextByID(keyFacts)); err != nil {
			if errors.Is(err, llm.ErrUnavailable) {
				return nil, fmt.Errorf("%w: stance %d: %w", ErrStanceRefreshPending, matched.ID, err)
			}
			log.Warn().Err(err).
				Int64("stance_id", matched.ID).
				Msg("Stance metadata refresh failed; stance kept with previous label/summary")
		}
	}

	return result, nil
}

// applyStanceUpdate writes the centroid + key fact IDs for a stance after a
// fact has joined; the fact count follows the joined row in the database.
// Summary is left as the existing one — the refresh call
// (triggerStanceRefresh) is responsible for regenerating it.
func (sm *StanceManager) applyStanceUpdate(ctx context.Context, matched *StanceDocument, factID int64, embedding []float32) error {
	newCentroid := updateCentroid(matched.ClusterEmbedding, embedding, matched.FactCount)
	keyFactIDs := matched.KeyFactIDs
	if len(keyFactIDs) < 10 {
		keyFactIDs = append(keyFactIDs, factID)
	}
	return sm.storer.UpdateStanceDocument(ctx, matched.ID, matched.Summary, matched.SummaryEn, newCentroid, keyFactIDs)
}

// verifiedContradictionsInStance treats `fact` as if it just arrived in
// `stance`: it runs the analyze + verification path against `otherFacts` and
// returns the verified contradictions without recording them. Used by the
// merge sweep, which analyzes the facts a merge will co-locate before
// committing it, and records the result only once the merge is applied.
//
// Unlike ProcessNewFact this skips assignTopic (we already KNOW the stance the
// fact belongs to) and writes nothing.
func (sm *StanceManager) verifiedContradictionsInStance(
	ctx context.Context,
	stance StanceDocument,
	fact StanceFactInfo,
	otherFacts []StanceFactInfo,
) ([]StanceContradictionEntry, error) {
	if len(otherFacts) == 0 {
		return nil, nil
	}
	agg, err := sm.analyzeAcrossBatches(ctx, stance.Summary, otherFacts, fact.NormalizedFactText, fact.Date)
	if err != nil {
		return nil, fmt.Errorf("analyze: %w", err)
	}
	if agg.PrimaryVerdict != "CONTRADICTION" {
		return nil, nil
	}
	// Adversarial verification pass (same gate as the live ingest path in
	// handleVerdict).
	return sm.verifyEntries(ctx, stance.PersonID, fact.ID, statementOf(fact), keyFactByID(otherFacts), agg.Contradictions)
}

// VerifyContradiction runs the adversarial refutation pass over a single
// claimed contradiction before it is persisted. keep == true means the claim
// survived (record it); keep == false means it was refuted as a false positive
// (drop it). A non-nil error is an operational failure — callers fall open,
// except on llm.ErrUnavailable, where they decide nothing.
//
// Delegates to llm.VerifyContradiction so the stance path and the detector path
// (contradiction.OpenAIContradictionDetector.VerifyContradiction) share
// identical refutation logic. The stance path judges plain texts (no group
// members) because the stance analysis that found the contradiction saw
// plain texts too: a veto never sees less than the finder.
func (sm *StanceManager) VerifyContradiction(ctx context.Context, a, b promptrules.Statement, claimedReason string) (keep bool, reason string, err error) {
	return llm.VerifyContradiction(ctx, sm.client, a, b, claimedReason)
}

// verifyEntries runs the adversarial refutation pass over the flagged
// contradiction entries, in their order, and returns the first survivor
// alone: a fact contradicts a stance once. The entries list every key fact
// of the stance the new fact opposes (the analysis is asked to be complete,
// clearest opposition first), but they are one inconsistency, the one between
// the fact and the stance: on 2026-10-27 one fact of Retailleau ("ne pense pas
// du tout à 2027") made 40 contradictions, one per later fact on his
// candidacy, and the repetition penalty cost a sixth of his score. The entries
// after the survivor are not judged, which spares their verifier calls.
//
// factID and newFactText are the fact being processed; keyTexts maps each key
// fact the analysis was shown to its text. Nothing is recorded unverified:
//   - an entry whose key fact the analysis was not shown is dropped: the
//     model gave an ID it cannot have read (prod #1205 paired a fact with the
//     reason of another);
//   - on a verifier error other than llm.ErrUnavailable the entry is not
//     kept but queued for the contradiction detector (fact_pair_queue), which
//     judges and verifies the pair on its own;
//   - an explicit refutation (keep=false) drops it.
//
// When the LLM is unavailable nothing is decided: the error (wrapping
// llm.ErrUnavailable) is returned and the caller must record nothing.
func (sm *StanceManager) verifyEntries(ctx context.Context, personID, factID int64, newFact promptrules.Statement, keyFacts map[int64]StanceFactInfo, entries []StanceContradictionEntry) ([]StanceContradictionEntry, error) {
	if len(entries) == 0 {
		return entries, nil
	}
	survivors := make([]StanceContradictionEntry, 0, 1)
	for i, c := range entries {
		if len(survivors) > 0 {
			log.Info().Int64("fact_id", factID).Int64("key_fact_id", survivors[0].KeyFactID).Int("not_judged", len(entries)-i).
				Msg("One contradiction per fact and stance: the other flagged key facts are not judged")
			break
		}
		keyFact, ok := keyFacts[c.KeyFactID]
		if !ok || strings.TrimSpace(keyFact.NormalizedFactText) == "" {
			log.Warn().Int64("fact_id", factID).Int64("key_fact_id", c.KeyFactID).Str("flagged_reason", c.Reason).
				Msg("Contradiction with a key fact the analysis was not shown; dropping")
			continue
		}
		keep, reason, err := sm.VerifyContradiction(ctx, newFact, statementOf(keyFact), c.Reason)
		if errors.Is(err, llm.ErrUnavailable) {
			return nil, fmt.Errorf("verifying contradiction with key fact %d: %w", c.KeyFactID, err)
		}
		if err != nil {
			log.Warn().Err(err).Int64("fact_id", factID).Int64("key_fact_id", c.KeyFactID).
				Msg("Contradiction verification failed; the pair goes to the detector")
			if qErr := sm.storer.EnqueueFactPair(ctx, personID, factID, c.KeyFactID, "stance:unverified"); qErr != nil {
				log.Warn().Err(qErr).Int64("fact_id", factID).Int64("key_fact_id", c.KeyFactID).Msg("Queueing the unverified pair failed")
			}
			continue
		}
		if !keep {
			log.Info().
				Int64("key_fact_id", c.KeyFactID).
				Str("flagged_reason", c.Reason).
				Str("refutation", reason).
				Msg("Contradiction refuted by verification pass; dropping (stance path)")
			metrics.RecordDecision(metrics.StageContradiction, metrics.OutcomeContradictionRefuted)
			continue
		}
		survivors = append(survivors, c)
	}
	return survivors, nil
}

// recordStanceContradictions writes each contradiction entry to the store,
// skipping invalid entries (KeyFactID <= 0) and self-pairs (KeyFactID == factID).
// Per-entry RecordContradiction errors are logged but do not abort the loop.
// Returns the number successfully recorded.
//
// Shared by handleVerdict (live ingest path) and applyMergeSubgroup
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
		// The bundler-needed signal is dropped: both facts are in the same
		// stance OF THIS PERSON, so the person-scoped fast path in storage
		// always sets bundle_stance_id directly.
		if rs, ok := sm.storer.(RestatementStorer); ok {
			if restated, _ := RestatesExisting(ctx, rs, sm.restate, "", personID, factID, c.KeyFactID, c.Reason); restated > 0 {
				continue
			}
		}
		if _, _, err := sm.storer.RecordContradictionImmediate(ctx, personID, factID, c.KeyFactID, c.Reason, c.ReasonEn); err != nil {
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

// RefreshStanceMetadata brings a stance's label + summary up to date with its
// facts. Used by code outside ProcessNewFact (post-merge sweep, deferred
// backfill refresh, the refresh CLI). Cost is bounded by what is NEW: when the
// whole fact list fits the model context it is rebuilt from scratch (the most
// accurate form, and a periodic drift reset for the incremental path); when it
// does not, only the facts added since summarized_at are folded into the
// current label + summary, in context-sized chunks. A stance therefore never
// becomes un-refreshable however large it grows (mirror-t1z).
func (sm *StanceManager) RefreshStanceMetadata(ctx context.Context, stanceID int64) error {
	return sm.triggerStanceRefresh(ctx, stanceID)
}

// RebuildStanceMetadata discards the current label + summary and re-derives
// them from every fact in the stance: one full-rebuild call when the facts fit
// the context, otherwise a rolling fold over all facts starting from an empty
// summary. For `dm stances refresh-metadata --full`.
func (sm *StanceManager) RebuildStanceMetadata(ctx context.Context, stanceID int64) error {
	facts, err := sm.storer.GetFactsForStance(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading facts for rebuild: %w", err)
	}
	if len(facts) == 0 {
		return nil
	}
	if fullRefreshFits(facts) {
		return sm.persistFullRefresh(ctx, stanceID, facts)
	}
	tensions, err := sm.storer.GetStanceTensions(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading tensions for rebuild: %w", err)
	}
	return sm.persistIncrementalRefresh(ctx, stanceID, "", "", newFactsFromStanceFacts(facts, tensions))
}

// deferRefreshKey is the context key used by WithDeferRefresh to tell
// handleVerdict to skip the per-fact refresh inside ProcessNewFact. Callers
// processing many facts in a row (the rebuild/backfill loop) opt in so each
// touched stance is refreshed ONCE at the end of the batch rather than after
// every NEW_INFO / CONTRADICTION verdict — a typical heavy person with 40
// facts in one stance goes from 40 refresh calls to 1.
type deferRefreshKey struct{}

// WithDeferRefresh returns a derived context that disables the inline
// refreshAfterVerdict call inside handleVerdict. The caller becomes
// responsible for calling RefreshStanceMetadata on every touched stance at
// the end of the batch.
//
// Safe to call on a context already wrapped with WithDeferRefresh — idempotent.
// Live ingest (FactInserter) does NOT wrap the context, so refresh remains
// inline for the facts that go through ProcessNewFact (news, twitter).
func WithDeferRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferRefreshKey{}, true)
}

// refreshDeferred returns true when the context was wrapped via WithDeferRefresh.
func refreshDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(deferRefreshKey{}).(bool)
	return v
}

// Metadata-refresh budgeting. The full-rebuild prompt inlines every fact; it is
// only used when that fits the model context (see fullRefreshFits). The
// incremental prompt carries the current label + summary plus the NEW facts,
// folded in chunks sized by refreshFactsBudgetTokens.
const (
	stanceRefreshOutputTokens = 700
	// stanceRefreshPromptTokens covers the fixed prose of the refresh prompts
	// plus the current label + summary carried by the fold prompt. Measured
	// 2026-09-16 with empty inputs, after the label rules became the shared
	// union block: fold prompt 5396 chars = 1349 tokens at stanceCharsPerToken,
	// full-rebuild prompt 1152 tokens; the ~600-token remainder holds the
	// current summary (2-4 sentences, ~150 tokens).
	stanceRefreshPromptTokens = 1950
	// stanceCompactionEvery: on the live path, every Nth fact of a stance
	// triggers a full rebuild instead of a fold when the facts fit the context,
	// so small and medium stances get a periodic drift reset for free while
	// large ones stay on the incremental path.
	stanceCompactionEvery = 10
)

// StanceTension is one recorded contradiction between a fact of a stance and
// another fact of the same stance, as the storer reports it.
type StanceTension struct {
	FactID        int64  // the fact this tension is attached to
	OtherFactText string // the fact it contradicts
	Reason        string
}

// refreshNewFact is one fact being folded into a stance's label + summary,
// with the tensions known for it (key facts it contradicts).
type refreshNewFact struct {
	ID          int64
	Text        string
	Verb        string
	Contradicts []refreshTension
}

type refreshTension struct {
	EarlierFactText string
	Reason          string
}

// newFactsFromStanceFacts renders pending facts for the fold prompt, attaching
// the tensions recorded for each.
func newFactsFromStanceFacts(facts []StanceFactInfo, tensions map[int64][]StanceTension) []refreshNewFact {
	out := make([]refreshNewFact, len(facts))
	for i, f := range facts {
		nf := refreshNewFact{ID: f.ID, Text: f.NormalizedFactText, Verb: f.CanonicalVerb}
		for _, t := range tensions[f.ID] {
			nf.Contradicts = append(nf.Contradicts, refreshTension{EarlierFactText: t.OtherFactText, Reason: t.Reason})
		}
		out[i] = nf
	}
	return out
}

// refreshFactsBudgetTokens is the fact-text budget of one refresh call.
func refreshFactsBudgetTokens() int {
	return stanceContextTokens(llm.TaskStanceLabel) - stanceRefreshOutputTokens - stanceContextSafetyTokens - stanceRefreshPromptTokens
}

// fullRefreshFits reports whether inlining every fact into the full-rebuild
// prompt stays within the model context.
func fullRefreshFits(facts []StanceFactInfo) bool {
	total := 0
	for _, f := range facts {
		total += estimateStanceTokens(formatKeyFactLine(f))
	}
	return total <= refreshFactsBudgetTokens()
}

// chunkNewFactsByBudget splits new facts into fold-sized chunks. Never emits an
// empty chunk, so a single oversized fact is still attempted alone.
func chunkNewFactsByBudget(facts []refreshNewFact) [][]refreshNewFact {
	budget := refreshFactsBudgetTokens()
	var chunks [][]refreshNewFact
	var cur []refreshNewFact
	curTokens := 0
	for _, f := range facts {
		ft := estimateStanceTokens(formatRefreshNewFact(f))
		if len(cur) > 0 && curTokens+ft > budget {
			chunks = append(chunks, cur)
			cur = nil
			curTokens = 0
		}
		cur = append(cur, f)
		curTokens += ft
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// triggerStanceRefresh brings the stance's label + summary up to date: full
// rebuild when every fact fits the context, otherwise an incremental fold of
// the facts added since summarized_at (chunked, with the tensions recorded
// for them). No-op when nothing is new.
func (sm *StanceManager) triggerStanceRefresh(ctx context.Context, stanceID int64) error {
	return sm.refreshStance(ctx, stanceID, true, nil)
}

// refreshAfterVerdict is the live-ingest refresh, O(1) in the stance size in
// the steady state: it folds the facts still pending for the stance — normally
// just the one that was analyzed, plus any earlier fact whose fold failed —
// into the current label + summary, carrying the tensions the analyze just
// verified for the new fact. Every stanceCompactionEvery-th fact it does a
// full rebuild instead when the facts fit the context (a drift reset for the
// small/medium stances that make up 99% of the table). A stance that was
// never summarized (summarized_at NULL, e.g. right after the migration) is
// summarized from ALL its facts rather than folding onto its stale
// placeholder.
func (sm *StanceManager) refreshAfterVerdict(ctx context.Context, matched *StanceDocument, factID int64, verified []StanceContradictionEntry, keyTexts map[int64]string) error {
	var live []StanceTension
	for _, c := range verified {
		if txt, ok := keyTexts[c.KeyFactID]; ok && txt != "" {
			live = append(live, StanceTension{FactID: factID, OtherFactText: txt, Reason: c.Reason})
		}
	}
	compact := (matched.FactCount+1)%stanceCompactionEvery == 0
	return sm.refreshStance(ctx, matched.ID, compact, live)
}

// refreshStance is the shared refresh engine. compact asks for a full rebuild
// when the facts fit; extraTensions are tensions known to the caller that may
// not be in the contradictions table yet (the live path records them just
// before, but the map is merged either way).
func (sm *StanceManager) refreshStance(ctx context.Context, stanceID int64, compact bool, extraTensions []StanceTension) error {
	facts, err := sm.storer.GetFactsForStance(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading facts for refresh: %w", err)
	}
	if len(facts) == 0 {
		return nil // nothing to refresh from
	}
	pending, err := sm.storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading unsummarized facts for refresh: %w", err)
	}
	fromScratch := len(pending) == len(facts)

	if (compact || fromScratch) && fullRefreshFits(facts) {
		return sm.persistFullRefresh(ctx, stanceID, facts)
	}
	if len(pending) == 0 {
		return nil
	}

	tensions, err := sm.storer.GetStanceTensions(ctx, stanceID)
	if err != nil {
		return fmt.Errorf("loading tensions for refresh: %w", err)
	}
	if tensions == nil {
		tensions = map[int64][]StanceTension{}
	}
	for _, t := range extraTensions {
		if !hasTension(tensions[t.FactID], t) {
			tensions[t.FactID] = append(tensions[t.FactID], t)
		}
	}

	label, summary := "", ""
	if !fromScratch {
		label, summary, err = sm.storer.GetStanceMetadata(ctx, stanceID)
		if err != nil {
			return fmt.Errorf("loading stance %d metadata: %w", stanceID, err)
		}
	}
	return sm.persistIncrementalRefresh(ctx, stanceID, label, summary, newFactsFromStanceFacts(pending, tensions))
}

func hasTension(list []StanceTension, t StanceTension) bool {
	for _, x := range list {
		if x.OtherFactText == t.OtherFactText {
			return true
		}
	}
	return false
}

// persistFullRefresh rebuilds label + summary from the full fact list and
// stores them.
func (sm *StanceManager) persistFullRefresh(ctx context.Context, stanceID int64, facts []StanceFactInfo) error {
	label, summary, err := sm.refreshStanceMetadata(ctx, stanceID, facts)
	if err != nil {
		return fmt.Errorf("regenerating metadata: %w", err)
	}
	ids := make([]int64, len(facts))
	for i, f := range facts {
		ids[i] = f.ID
	}
	if err := sm.storeRefreshedMetadata(ctx, stanceID, label, summary, ids); err != nil {
		return err
	}
	log.Debug().Int64("stance_id", stanceID).Str("new_label", label).Int("facts", len(facts)).Msg("Refreshed stance metadata (full rebuild)")
	return nil
}

// persistIncrementalRefresh folds newFacts into (label, summary) chunk by
// chunk — each chunk's output is the next chunk's input — and stores the
// result. Empty label + summary means "start from scratch".
func (sm *StanceManager) persistIncrementalRefresh(ctx context.Context, stanceID int64, label, summary string, newFacts []refreshNewFact) error {
	chunks := chunkNewFactsByBudget(newFacts)
	for _, chunk := range chunks {
		var err error
		label, summary, err = sm.foldStanceMetadata(ctx, label, summary, chunk)
		if err != nil {
			return fmt.Errorf("folding %d facts into stance %d metadata: %w", len(chunk), stanceID, err)
		}
	}
	ids := make([]int64, len(newFacts))
	for i, f := range newFacts {
		ids[i] = f.ID
	}
	if err := sm.storeRefreshedMetadata(ctx, stanceID, label, summary, ids); err != nil {
		return err
	}
	log.Debug().Int64("stance_id", stanceID).Str("new_label", label).Int("new_facts", len(newFacts)).Int("chunks", len(chunks)).Msg("Refreshed stance metadata (incremental fold)")
	return nil
}

func (sm *StanceManager) storeRefreshedMetadata(ctx context.Context, stanceID int64, label, summary string, consumedFactIDs []int64) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("refresh returned empty label for stance %d", stanceID)
	}
	if r := []rune(label); len(r) > 120 {
		label = string(r[:120])
	}
	if err := sm.storer.UpdateStanceMetadata(ctx, stanceID, label, strings.TrimSpace(summary), consumedFactIDs); err != nil {
		return fmt.Errorf("persisting refreshed metadata: %w", err)
	}
	return nil
}

// callJSONLLM is the shared boilerplate for sending a JSON-output chat request
// to the LLM (Chat already strips a code fence in JSON mode). Callers
// json.Unmarshal the returned content into their own response type; task sets
// the thinking setting (llm/task.go).
//
// Used by the stance analysis, topic assignment, labels, metadata refresh and
// the merge decisions — sites that share the same request envelope
// (system + user message, JSON response format, configurable temperature +
// max tokens) and the same response shape (first chat choice → strip fences).
func (sm *StanceManager) callJSONLLM(
	ctx context.Context,
	systemMsg, userPrompt string,
	temperature float64,
	maxTokens int,
	task llm.Task,
) (string, error) {
	req := llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: systemMsg},
			{Role: "user", Content: userPrompt},
		},
		Temperature:    temperature,
		MaxTokens:      maxTokens,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           task,
	}
	content, _, err := sm.client.Chat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	return strings.TrimSpace(content), nil
}

// stanceAnalysisPromptTpl is the prompt of the stance analysis: the stance
// summary, the key facts and the new fact fill its three %s. Its text is the
// judge of the pairs the analysis records (StanceJudgeHash).
var stanceAnalysisPromptTpl = `You are analyzing whether a new fact about a political figure updates, duplicates, or contradicts their known stance on a topic.

The EXISTING STANCE SUMMARY, the KEY FACTS IN THIS STANCE and the NEW FACT are given after the rules.

Decide ONE primary_verdict for the NEW FACT vs the stance:
1. DUPLICATE — the new fact says essentially the same thing as one of the key facts (paraphrase / same claim, different source). Set duplicate_of_key_fact_id to that key fact's [ID N].
2. NEW_INFO — the new fact adds new information to this topic without contradicting any key fact.
3. CONTRADICTION — the new fact DIRECTLY contradicts ONE OR MORE specific key facts per the rules below.

**COMPLETENESS REQUIREMENT (THIS IS CRITICAL — read before anything else):**
You MUST evaluate the NEW FACT against EVERY key fact in the stance, INDEPENDENTLY, and list EVERY key fact that is directly contradicted as a SEPARATE entry in the contradictions array. Do NOT deduplicate based on:
- Similarity between key facts (if three key facts each separately contradict the new fact, return three entries)
- Perceived "strength" of one contradiction over another (a weaker direct contradiction is still a contradiction; list it)
- Same underlying axis or rule (multiple key facts on the same axis can each independently contradict the new fact)
The downstream system records the first entry that survives verification as the contradiction between the new fact and this stance, so ORDER the entries from the clearest opposition to the least clear; a missing entry can leave a weaker one in front. Err on the side of listing more direct edges, never fewer.

**COMPLETENESS DOES NOT OVERRIDE THE NEGATIVE-PATTERN CARVE-OUTS.** Completeness only fires on pairs that pass the contradiction rules below. If a pair falls under a NEGATIVE PATTERN (ADMINISTRATIVE ERROR vs CRIMINAL INTENT — PAIR 16/17 in the examples; COHERENT DEFENSE — PAIR 18; CONSISTENT IGNORANCE; SAME POSITION IN DIFFERENT WORDS; ANNOUNCEMENT FOLLOWED BY ACTION; etc.), DO NOT emit an entry for it — even if every other heuristic says "but they sound opposite". The carve-outs win.

Specifically:
- "Acknowledged things could have been more rigorous" / "an administrative error was made" / "acted in good faith despite negligence" + "denied any wrongdoing / denied criminal intent" → NOT a contradiction (PAIR 16/17). Do NOT add this edge under completeness.
- Multiple "I didn't know X" / "I didn't know Y" denials about different aspects of the same case → NOT contradictory (CONSISTENT IGNORANCE / COHERENT DEFENSE — PAIR 17/18). Do NOT add edges between them.
- A promise to do X + later actually doing X → fulfilled commitment, NOT contradictory (ANNOUNCEMENT THEN ACTION — PAIR 24).
- Paraphrases of the same position → NOT contradictory (SAME POSITION IN DIFFERENT WORDS — PAIR 20).

Example of correct multi-edge output: if the NEW FACT is "authorised offshoring of a French factory to China" and the stance contains three earlier key facts — [ID 12] "promised to rebuild French artisanat", [ID 17] "voted for tariffs on Chinese imports", [ID 23] "defending French industrial jobs in a Lille speech" — each of those three contradicts the new fact INDEPENDENTLY (each is its own direct opposition between two statements). Return THREE contradiction entries, one per pair.

Counter-example (completeness with a carve-out): if the NEW FACT is "denied any criminal wrongdoing in the parliamentary assistants case" and the stance contains key facts — [ID 50] "acknowledged that the management could have been more rigorous", [ID 51] "court of appeal convicted her for the same case" — only [ID 51] is a contradiction (CLAIM ↔ AUTHORITATIVE FINDING). [ID 50] is NOT a contradiction (CONVICTION vs ACKNOWLEDGMENT OF IMPERFECTIONS carve-out, PAIR 17). Return ONE contradiction entry, not two. Completeness does not force you to add the carved-out pair.

**DIRECT-EDGE REQUIREMENT (the precision brake — apply to each candidate entry):**
Every contradiction entry MUST be a DIRECT opposition between the NEW FACT and the named key fact (the person's own positions, claims, commitments or acts conflict, a position reversal, or an authoritative finding against the person's denial) — the pair must be one you would flag as contradictory if you saw only those two statements side by side. Surrounding key facts may be used to verify that a stated CONDITION has been met or a TRIGGER has fired (PAIR 5b / Commitment vs Carve-Out style — the other facts establish that the condition is real, but the contradiction edge is still between the two named statements alone). They may NOT be used to bridge two facts that are themselves aligned. If the new fact and a key fact both move in the SAME DIRECTION on the same subject (both commit to outcome X, both deny outcome Y, both defend the same value), they CANNOT contradict each other — even if a third key fact in the stance moves in the opposite direction. In that case the third key fact contradicts each of them separately; the two aligned facts do not contradict each other.

**ONE REASON PER KEY FACT:** each entry's reasoning and reason are about the NEW FACT and THAT key fact only: they quote or name only what those two statements say. Never give two entries the same reason, never cite in an entry a word or an event of another key fact; if the reason you would write fits another key fact better, the entry belongs to that key fact's ID.

**Mandatory mental check before emitting each contradiction entry:** ask "If I only had the NEW FACT and this one key fact in front of me, with no other context, would I still flag them as contradictory?"
- If YES → emit the entry (regardless of how many other entries you've already emitted; completeness wins).
- If NO — if you only see the contradiction because of a third key fact — DROP the entry. The third key fact may be its own contradiction with the new fact (record THAT pair instead).

The COMPLETENESS and DIRECT-EDGE rules are complementary: COMPLETENESS says "don't drop direct edges to avoid redundancy", DIRECT-EDGE says "don't add transitive edges to seem thorough". Both together: list every pair the mental check says YES on, no more, no less.

` + promptrules.ContradictionRules + `

**HOW TO MAP THE PAIR-WISE EXAMPLES TO THIS TASK:**
The examples above are framed as "PAIR N: YES/NO". Treat YES as a contradiction entry (one per DIRECTLY contradicted key fact — see DIRECT-EDGE REQUIREMENT above). Treat NO as either DUPLICATE (same position, different words — set duplicate_of_key_fact_id) or NEW_INFO (compatible-but-distinct).

EXISTING STANCE SUMMARY:
%s

KEY FACTS IN THIS STANCE (each line is "[ID N] (verb) (dated YYYY-MM-DD) text", "(undated)" when the date is unknown; the NEW FACT carries its date the same way — use them for the TEMPORAL REASONING rules):
%s

NEW FACT:
%s

**RESPONSE FORMAT:**
Compare the NEW FACT against EACH key fact independently before deciding the primary verdict. For each contradiction entry, write its per-pair reasoning BEFORE its verdict — and apply the mental check from the DIRECT-EDGE REQUIREMENT to that entry. Put the clearest opposition first in the array.

**LANGUAGE RULE:** Write each contradiction's ` + "`reason`" + ` in FRENCH (even when the facts are in English). Do NOT provide any translation — English and other languages are produced separately by the translation phase. ` + "`reasoning`" + ` fields (both top-level and per-contradiction) may follow the language of the facts.

Return ONLY valid JSON:
{
  "reasoning": "1-2 sentences justifying the PRIMARY VERDICT (DUPLICATE / NEW_INFO / CONTRADICTION) for this new fact against the stance as a whole — NOT a list of every contradiction (those have their own reasoning fields below)",
  "contradictions": [
    {
      "reasoning": "1-2 sentences: restate the NEW FACT's core claim, restate THIS key fact's core claim, then explain why these two are directly opposed when considered in isolation. If you find yourself naming a third key fact's ID to explain the opposition, the DIRECT-EDGE REQUIREMENT fails — drop this entry.",
      "reason": "1-sentence user-facing summary in French of why these two facts contradict",
      "key_fact_id": <id>
    }
  ],
  "duplicate_of_key_fact_id": 0,
  "primary_verdict": "DUPLICATE|NEW_INFO|CONTRADICTION"
}

For DUPLICATE: contradictions MUST be []. Set duplicate_of_key_fact_id to the [ID N] of the key fact the new fact paraphrases.
For NEW_INFO: contradictions MUST be []. Set duplicate_of_key_fact_id to 0.
For CONTRADICTION: contradictions MUST contain at least one entry; set duplicate_of_key_fact_id to 0.`

// callLLM sends the stance analysis prompt for ONE batch of key facts to the LLM
// and parses the response. Callers split into batches via chunkKeyFacts; this
// function handles a single batch and returns whatever contradictions the LLM
// found within it.
func (sm *StanceManager) callLLM(ctx context.Context, stanceSummary string, keyFacts []StanceFactInfo, newFactText string, newFactDate *time.Time) (*stanceLLMResponse, error) {
	var factsBuilder strings.Builder
	for _, f := range keyFacts {
		factsBuilder.WriteString(formatKeyFactLine(f))
	}

	prompt := fmt.Sprintf(stanceAnalysisPromptTpl,
		stanceSummary, factsBuilder.String(), datedPrefix(newFactDate)+newFactText)

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
		// Thinking is set by llm.TaskStanceAnalyze in llm/task.go. History:
		// Gemma's thinking regressed recall (-0.14, eval v17 vs v16) on
		// values_based pairs the COMPLETENESS rule otherwise catches.
		// Output reservation: stanceAnalyzeOutputTokens (2048). Lowered from 4000
		// because the ~8K-token base prompt + 4K output exactly filled the 12K
		// context, leaving no room for key facts and overflowing (facts were
		// rolled back). chunkKeyFactsByBudget now bounds facts (and thus the
		// contradiction-entry count) per call, so 2048 is ample.
		content, err := sm.callJSONLLM(ctx, sys, prompt, t, stanceAnalyzeOutputTokens, llm.TaskStanceAnalyze)
		if err != nil {
			lastErr = fmt.Errorf("LLM call: %w", err)
			if ctx.Err() != nil {
				// A deadline or a cancel cut the call: a retry fails the same way.
				return nil, lastErr
			}
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

// truncate keeps the first maxLen runes of s and appends "..." when it cut.
// Kept separate from util.TruncateString (which fits the suffix inside
// maxLen): the stance prompts below embed the result, and changing their
// bytes would invalidate the vLLM prefix cache and the golden fixtures.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

// assignTopic routes a new fact to one of the person's existing stances or
// declares it new. It shortlists candidate stances by their NEAREST MEMBER FACT
// to the fact's embedding (not by centroid), then hands those candidates — each
// carrying its closest member facts as concrete evidence — to the LLM via
// assignTopicFromCandidates. The nearest-member-fact ranking is the fix for the
// mega-stance attractor: a large catch-all's averaged centroid sits moderately
// close to everything, so centroid ranking vacuumed loosely related facts into
// it; ranking by nearest member fact does not.
//
// When the fact has no embedding, the person has no stances, or the shortlist
// comes back empty, assignTopic degrades to a label/summary-only shortlist built
// from the passed stances so it never crashes — same LLM call, weaker evidence.
// assignExclude narrows a re-assignment: StanceID is a stance the fact
// currently belongs to and must not be offered again (the singleton sweep;
// stances is then the person's stances without it); FactID is the fact
// itself, hidden from the candidates' evidence (the placement check, where
// its current stance stays a candidate but must earn it on its other facts).
type assignExclude struct {
	StanceID int64
	FactID   int64
}

func (sm *StanceManager) assignTopic(ctx context.Context, personID int64, stances []StanceDocument, factText string, factEmbedding []float32, excl assignExclude) (*topicAssignmentDecision, error) {
	var candidates []StanceCandidate
	var labelOnly map[int64]bool
	if len(factEmbedding) > 0 && len(stances) > 0 {
		shortlisted, err := sm.storer.ShortlistStancesByNearestFact(ctx, personID, factEmbedding, shortlistTopK, shortlistFactsPerStance, excl.FactID)
		if err != nil {
			return nil, fmt.Errorf("shortlisting stances by nearest fact: %w", err)
		}
		for _, c := range shortlisted {
			if c.ID != excl.StanceID {
				candidates = append(candidates, c)
			}
		}

		// The embedding shortlist ranks stances by their nearest member fact,
		// in the stance-blind topic-slug space; for a person with more stances
		// than the shortlist holds, a fitting stance is often not in it at all
		// (measured on prod: 7 of 36 singleton creations had the right home
		// outside the top 8, and ranking by centroid did no better). The label
		// pass reads the person's labels instead and adds the ones it picks.
		if len(stances) > shortlistTopK {
			picked, err := sm.pickTopicsByLabel(ctx, factText, stances)
			if errors.Is(err, llm.ErrUnavailable) {
				// Assigning on the shortlist alone would make the outage's
				// miss final: the fact would open a new stance for good.
				return nil, fmt.Errorf("topic label pass: %w", err)
			}
			if err != nil {
				log.Warn().Err(err).Int64("person_id", personID).Msg("Topic label pass failed; using the embedding shortlist alone")
			} else if extra := excludeIDs(picked, candidates); len(extra) > 0 {
				added, err := sm.storer.StanceCandidatesByIDs(ctx, personID, factEmbedding, extra, shortlistFactsPerStance, excl.FactID)
				if err != nil {
					return nil, fmt.Errorf("loading label-picked stance candidates: %w", err)
				}
				labelOnly = make(map[int64]bool, len(added))
				for _, c := range added {
					labelOnly[c.ID] = true
				}
				candidates = append(candidates, added...)
			}
		}
	}

	// Fallback: no embedding, no stances, or empty shortlist. Build candidates
	// from the passed stances (label + summary only, no member facts) so the LLM
	// still classifies against the full topic list instead of crashing.
	if len(candidates) == 0 {
		candidates = make([]StanceCandidate, len(stances))
		for i, s := range stances {
			candidates[i] = StanceCandidate{
				ID:        s.ID,
				Label:     s.ClusterLabel,
				Summary:   s.Summary,
				FactCount: s.FactCount,
			}
		}
	}

	decision, err := sm.assignTopicFromCandidates(ctx, factText, candidates)
	if err == nil && decision != nil && !decision.IsNew {
		decision.ViaLabelPass = labelOnly[decision.AssignedID]
	}
	return decision, err
}

// Shortlist sizes for topic assignment: the embedding shortlist's length and
// the member facts shown per candidate, and how many stances the label pass
// may add on top.
const (
	shortlistTopK           = 8
	shortlistFactsPerStance = 6
	labelPickMax            = 3
)

// excludeIDs returns the ids not already among the candidates, in order.
func excludeIDs(ids []int64, candidates []StanceCandidate) []int64 {
	present := make(map[int64]bool, len(candidates))
	for _, c := range candidates {
		present[c.ID] = true
	}
	var out []int64
	for _, id := range ids {
		if !present[id] {
			present[id] = true
			out = append(out, id)
		}
	}
	return out
}

// topicLabelPickResponse is the JSON the label pass returns.
type topicLabelPickResponse struct {
	Reasoning string `json:"reasoning"`
	Indexes   []int  `json:"topic_indexes"` // 1-indexed into the label list; empty when none could fit
}

// pickTopicsByLabel asks which of the person's existing topics, by label
// alone, could house the fact, and returns up to labelPickMax stance ids in
// the order given. It is a recall pass: it may over-pick, the assignment
// prompt decides with the member facts in view. Rules first, data last, so
// the prefix is cached across persons.
func (sm *StanceManager) pickTopicsByLabel(ctx context.Context, factText string, stances []StanceDocument) ([]int64, error) {
	var labels strings.Builder
	for i, s := range stances {
		fmt.Fprintf(&labels, "%d. %s\n", i+1, s.ClusterLabel)
	}
	prompt := fmt.Sprintf(`You are a topic classifier for political facts. A person has the numbered list of existing topics below (labels only). Given a NEW FACT about that person, list every topic that could house it: the subject area the fact belongs to, or a broader area that contains it. This is a recall pass — a later step sees the topics' facts and decides — so include a topic when the fact is plausibly within its area, and return an empty list only when no listed topic is in the same domain.

RULES:
- A topic is a NEUTRAL SUBJECT AREA: "politique du logement" houses every housing measure (HLM rules, renovation aid, mortgage rules), whatever the person's position.
- A fact about one measure belongs to the topic of its policy domain even when the label names the domain generically ("politique fiscale" houses a tobacco tax freeze; "politique migratoire" houses retention-centre rules; "politique agricole" houses support to fisheries committees).
- Classify the CONTENT of the statement, never its venue or occasion: "lors de son passage sur BFM", "dans un meeting", "sur X" say where it was said, not what it is about. A remark on a foreign leader made on TV is foreign policy, not media relations.
- A stated principle, right or value applied to a policy field belongs to that field's topic: "la priorité nationale est un droit naturel" is about immigration and access to social benefits, not about political philosophy.
- Prefer the most specific topic that contains the fact; also list a broader one when both fit.
- Return at most %d topics, most likely first. Never invent a topic that is not in the list.

EXISTING TOPICS:
%sNEW FACT:
%s
Return ONLY valid JSON:
{"reasoning": "one sentence", "topic_indexes": [ ... ]}`, labelPickMax, labels.String(), factText)
	const sys = "You are a political fact topic classifier. Return only valid JSON."
	content, err := sm.callJSONLLM(ctx, sys, prompt, 0.0, 512, llm.TaskStanceAssign)
	if err != nil {
		return nil, fmt.Errorf("topic label pass: %w", err)
	}
	resp, err := llm.ParseJSONResponse[topicLabelPickResponse](content)
	if err != nil {
		return nil, fmt.Errorf("topic label pass: parsing response %q: %w", truncate(content, 200), err)
	}
	var ids []int64
	for _, idx := range resp.Indexes {
		if idx < 1 || idx > len(stances) {
			log.Warn().Int("index", idx).Int("labels", len(stances)).Msg("Topic label pass returned an index outside the list; ignoring it")
			continue
		}
		ids = append(ids, stances[idx-1].ID)
		if len(ids) == labelPickMax {
			break
		}
	}
	return ids, nil
}

func (sm *StanceManager) assignTopicFromCandidates(ctx context.Context, factText string, candidates []StanceCandidate) (*topicAssignmentDecision, error) {
	var topicsBuilder strings.Builder
	if len(candidates) == 0 {
		topicsBuilder.WriteString("(no existing topics — you MUST return assigned_topic_index=0 and emit a general new_topic_label per the LABEL RULES above)\n")
	} else {
		for i, c := range candidates {
			topicsBuilder.WriteString(fmt.Sprintf("%d. %q (%d facts): %s\n",
				i+1, c.Label, c.FactCount, truncate(c.Summary, 150)))
			for _, f := range c.MemberFacts {
				topicsBuilder.WriteString(fmt.Sprintf("   - %s\n", truncate(f, 220)))
			}
		}
	}

	prompt := fmt.Sprintf(`You are a topic classifier for political facts. Given a new fact and a list of existing topics about a person, decide which topic the fact belongs to. Each candidate topic shows its CLOSEST existing facts as evidence. When no topic fits you name a new one: the LABEL RULES govern that new_topic_label. The EXISTING TOPICS and the NEW FACT are given after the rules.

ASSIGNMENT RULES (ONE SUBJECT per topic — avoid BOTH multi-subject mega-buckets AND over-fragmentation into near-duplicate topics):
- A topic is a NEUTRAL SUBJECT AREA — the underlying issue, not a position on it. "Iran policy" covers BOTH support for and opposition to Iran-related actions; "politique des dépenses publiques" covers BOTH defending public services AND advocating austerity. The same topic label must work whether the person is pro, anti, mixed, or evolving on the subject.
- Each candidate shows its CLOSEST existing facts. They show WHICH DOMAIN the candidate covers — the new fact does not have to match them literally. Assign the new fact to a candidate when it is about the SAME SUBJECT AREA as those facts: the same named instrument (article 49.3), the same law, case or person, OR another measure within the same policy domain. Matching member facts are STRONG evidence; a vague resemblance to a label that names a DIFFERENT domain is not.
- ONE SUBJECT per stance. Never assign to (or create) a stance whose label glues together MULTIPLE distinct domains. BAD: "sécurité, justice et protection des mineurs" (three different subjects). If the new fact doesn't clearly belong to ONE single-subject candidate, create a NEW topic rather than dumping it into a broad multi-subject bucket.
- Assign the fact to an existing topic if it shares the SAME subject area, regardless of whether the new fact's direction agrees or disagrees with the existing topic's facts. Different events / different aspects / different positions on the same subject all belong together.
- **Polarity within a subject is CONTRADICTION MATERIAL, not a reason to fragment.** If the new fact opposes existing facts in a topic on the same subject area, assign it to that topic anyway. The internal tension is precisely what the contradiction detector exists to surface downstream. NEVER create a new topic just because the new fact's direction differs from the existing facts.
  * "promised to defend public hospitals" + later "voted for austerity cutting hospital subsidies" → SAME topic ("politique des dépenses publiques"), not two topics.
  * "promised 10%% teacher raise" + later "voted against teacher raise" → SAME topic ("politique salariale des enseignants").
  * "promised to renounce article 49.3" + later "triggered article 49.3 on the budget" → SAME topic (the budget / 49.3 procedure), not security or governance.
  * "rebuild French artisanat" + "authorise offshoring of French factory" + "tariffs on Chinese imports" → SAME topic ("souveraineté industrielle française").
- Only create a NEW topic when the fact is about a GENUINELY DIFFERENT SUBJECT AREA — not a different direction on the same subject. A "tax policy" fact and a "foreign policy" fact are different subjects. A "pro-environment" fact and an "anti-environment" fact are the SAME subject.
- When the new fact clearly matches ONE single-subject candidate, prefer assigning to it over creating a new topic. But a vague resemblance to a BROAD multi-subject bucket is NOT a match — create a new single-subject topic instead.
- DIFFERENT FACETS OF THE SAME SUBJECT BELONG TOGETHER — do NOT over-fragment. If an existing FOCUSED (single-subject) candidate covers the same broad area as the new fact, assign to it even if the specific event, sub-aspect, actor, or framing differs. A different facet of one domain is NOT a different subject. Creating a near-duplicate topic is just as wrong as a mega-bucket: it splits facts that should be compared and hides contradictions.
  * "interpellations par les forces de l'ordre" + "délinquance et violence urbaine" + "effectifs de la police" → ONE topic ("sécurité intérieure et forces de l'ordre"), not three.
  * "dépistage de stupéfiants des agents publics" + "prévention de la consommation de drogue" → ONE topic ("lutte contre les stupéfiants"), not two.
  * "aides sur le prix des carburants" + "soutien aux ménages face à la crise énergétique" → ONE topic ("aides énergie et carburants"), not two.
  * new fact "remplacer MaPrimeRénov' par un nouveau dispositif de rénovation" + existing "politique du logement" (HLM, loi SRU, accès au logement) → ASSIGN to "politique du logement": a different housing measure is the same domain.
  * new fact "obliger les plateformes à supprimer les fonctions d'addiction" + existing "souveraineté numérique et intelligence artificielle" → ASSIGN: platform regulation is another facet of digital policy.
  * new fact "les sondages à huit mois de l'élection ne disent rien" + existing "candidature à l'élection présidentielle de 2027" → ASSIGN: a remark on the polls of the race the person runs in belongs to the candidacy topic.
  * new fact "réduire l'usage des pesticides dans les exploitations" + existing "politique agricole" (revenus des agriculteurs, prix planchers) → ASSIGN: farming practices are another facet of agricultural policy.
  * new fact "livrer des armes à l'Arménie" + existing "relations diplomatiques et politique étrangère" whose facts are about Israel → ASSIGN: another country or another conflict is another facet of foreign policy, not a new domain.
  Reserve a NEW topic for a genuinely different DOMAIN, never for a different facet, event, or wording within the same domain. When an existing focused candidate is a reasonable home, USE IT.
- A SHARED WORD IS NOT A SHARED SUBJECT. The facet rule above needs the new fact to take a position on, or report an act about, the QUESTION the candidate's facts are about; a name, a country, an institution or a word in common is not enough. When the ONLY link is such a word, create a NEW topic:
  * new fact "la victoire de Rob Jetten aux législatives néerlandaises montre que les idées pro-européennes peuvent convaincre" + existing "élections européennes de 2024" (the person's list, campaign, score) → NEW: another country's national election is not the person's European campaign.
  * new fact "Bruno Le Maire devrait demander la nationalité américaine" + existing "dette publique et déficit" whose facts are about Le Maire's budgets → NEW: a jab at a person is not a position on the debt.
  * new fact "a confirmé sa présence à la messe du pape place de la Concorde" + existing "laïcité et islamisme" → NEW: attending a ceremony is not a position on laïcité.
- A fact that touches SEVERAL subjects (e.g. "contre la TVA sociale et contre l'entrée de l'Ukraine dans l'UE") is assigned by its MAIN subject — the one its first clause is about — to the matching single-subject candidate. NEVER create a multi-subject topic for it.

`+stanceLabelRules+`
- **LANGUAGE:** Write `+"`new_topic_label`"+` in the same language as the NEW FACT provided. Do not translate to English unless the fact itself is in English. If facts span multiple languages, use French.
- If the fact is itself a journalist's META observation about the entity, the upstream extractor should have rejected it; either pick the closest existing subject-bound topic or, if truly no fit, emit a SUBJECT-bound new label (never the meta phrasing).

EXISTING TOPICS:
%s
NEW FACT:
%s

Return ONLY valid JSON:
{
  "reasoning": "Brief explanation of your choice",
  "assigned_topic_index": 0,
  "new_topic_label": "broad subject-area label, max 120 chars (only if assigned_topic_index is 0)",
  "new_topic_label_en": "faithful English translation of new_topic_label — OMIT when new_topic_label is already English"
}

Set assigned_topic_index to the number of the matching topic (1-%d), or 0 if no existing topic fits.`, topicsBuilder.String(), factText, len(candidates))

	const sys = "You are a political fact topic classifier. Return only valid JSON."
	content, err := sm.callJSONLLM(ctx, sys, prompt, 0.1, 800, llm.TaskStanceAssign)
	if err != nil {
		return nil, fmt.Errorf("topic assignment: %w", err)
	}

	var resp topicAssignmentResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// One retry at a slightly higher temperature. Truncated/garbled JSON here
		// is the same transient failure mode callStanceJSONWithRetry handles for
		// the analyze call — during the 2026-07-24 rebuild it orphaned ~11% of
		// facts (1,243) because a single parse failure skipped the whole fact.
		log.Warn().Err(err).Str("llm_content", content).Msg("Topic assignment JSON parse failed; retrying once at T=0.3")
		content, err = sm.callJSONLLM(ctx, sys, prompt, 0.3, 800, llm.TaskStanceAssign)
		if err != nil {
			return nil, fmt.Errorf("topic assignment (retry): %w", err)
		}
		if err := json.Unmarshal([]byte(content), &resp); err != nil {
			log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse topic assignment response twice")
			return nil, fmt.Errorf("parsing topic assignment response: %w", err)
		}
	}

	decision := &topicAssignmentDecision{Reasoning: resp.Reasoning}
	// Map the LLM's 1-based candidate index to a stance ID. Index 0 or
	// out-of-range means a new topic.
	if resp.AssignedIndex >= 1 && resp.AssignedIndex <= len(candidates) {
		decision.AssignedID = candidates[resp.AssignedIndex-1].ID
	} else {
		decision.IsNew = true
		decision.NewLabel = strings.TrimSpace(string(resp.NewTopicLabel))
		decision.NewLabelEn = strings.TrimSpace(string(resp.NewTopicLabelEn))
	}
	return decision, nil
}

// SubjectLabel gives the general subject-area label of a fact, under the
// label rules of every stance label. The stance routing uses it when the
// assignment gives no usable label. A plugin can use it to give the same
// subject to facts on one matter, for example the ballots of one scrutin.
func (sm *StanceManager) SubjectLabel(ctx context.Context, factText string) (string, error) {
	prompt := fmt.Sprintf(`Generate a single GENERAL topic label for a political fact. The label is a NEUTRAL SUBJECT AREA broad enough that 5-20 related future facts — including facts that take OPPOSITE positions on this subject — can all fit under it. The FACT is given after the rules.

`+stanceLabelRules+`

FACT:
%s

Return ONLY valid JSON:
{"label": "subject-area label, max 120 chars"}`, factText)

	const labelSys = "You generate broad topic labels for political facts. Return only valid JSON."
	content, err := sm.callJSONLLM(ctx, labelSys, prompt, 0.1, 512, llm.TaskStanceLabel)
	if err != nil {
		return "", fmt.Errorf("label generation: %w", err)
	}
	var resp generalLabelResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// Same transient-truncation retry as assignTopicFromCandidates.
		log.Warn().Err(err).Str("llm_content", content).Msg("Label JSON parse failed; retrying once at T=0.3")
		content, err = sm.callJSONLLM(ctx, labelSys, prompt, 0.3, 512, llm.TaskStanceLabel)
		if err != nil {
			return "", fmt.Errorf("label generation (retry): %w", err)
		}
		if err := json.Unmarshal([]byte(content), &resp); err != nil {
			return "", fmt.Errorf("parsing label response: %w", err)
		}
	}
	label := strings.TrimSpace(string(resp.Label))
	if label == "" {
		return "", fmt.Errorf("label LLM returned empty label")
	}
	return label, nil
}

// refreshStanceMetadata regenerates a stance's cluster_label AND summary from
// the FULL set of facts currently attached to it — the compaction form of the
// refresh, used only when every fact fits the model context (persistFullRefresh);
// larger stances are folded incrementally by foldStanceMetadata.
//
// The prompt asks the LLM to:
//   - Re-derive the broadest accurate subject-area label per the LABEL RULES.
//   - Write a 2-4 sentence summary that PRESERVES tensions if any pair of key
//     facts conflict — do not smooth contradictions into one narrative.
func (sm *StanceManager) refreshStanceMetadata(ctx context.Context, stanceID int64, facts []StanceFactInfo) (string, string, error) {
	if len(facts) == 0 {
		return "", "", fmt.Errorf("refreshStanceMetadata: stance %d has no facts", stanceID)
	}
	var factsBuilder strings.Builder
	for _, f := range facts {
		factsBuilder.WriteString(fmt.Sprintf("- [ID %d] (%s) %s\n", f.ID, f.CanonicalVerb, f.NormalizedFactText))
	}

	prompt := fmt.Sprintf(`You are regenerating the topic label and summary for a stance document about a political figure. The stance contains the facts given after the rules; produce a fresh broad subject-area label and a 2-4 sentence summary that accurately reflects what's in the stance NOW.

`+stanceLabelRules+`

`+stanceSummaryRules+`

FACTS IN THIS STANCE (each line is "[ID N] (verb) text"):
%s

`+stanceRefreshLanguageAndFormat, factsBuilder.String())

	return sm.callRefreshLLM(ctx, prompt)
}

// stanceSummaryRules is the single source of what a stance summary is,
// shared by the full rebuild, the incremental refresh and the merge. A
// summary is read in a list of stances on a person's page, above the facts
// and their sources: it says the position at a glance. Before 2026-11-01 it
// listed measures, figures, dates and events (median 436 characters, 7579
// stances over 600, up to 4017), and the incremental refresh kept adding.
// The contradictions have their own cards: a summary does not judge them
// (the "tensions" it named were often not contradictions at all).
const stanceSummaryRules = `SUMMARY RULES:
- At most 2 sentences and about 300 characters. It says, at a glance, the person's POSITION on the subject and its direction (for, against, demanding, opposing, how it moved over time if it clearly did).
- High level only: no list of measures, no figures, no dates, no places, no names of laws or events, no quotes, unless one of them IS the position (a single flagship measure, the one figure the person campaigns on). The facts below the summary carry the details and their sources.
- Do not narrate what happened (announcements, events, who said what to whom): describe the stance.
- Do not judge or name contradictions, tensions, contrasts or paradoxes: the contradictions are shown separately.
- Every claim must be supported by the facts; no inference about motives.
- Describe the person, never the list below: no "les faits listés", "les faits relevés", "les éléments ci-dessus". The reader sees a position, not a list.
- No order in time between undated facts: "passé de X à Y", "d'abord, puis" need dated facts.
`

const stanceSummaryMaxRunes = 420

// stanceLabelRules is the single source of what a stance label may be, shared
// by topic assignment, the fallback label generator, the full rebuild and the
// incremental refresh so the five prompts can never drift. Prompt-specific
// rules (the label language, the keep-the-current-label rule of the fold)
// follow the block in each prompt.
const stanceLabelRules = `LABEL RULES:
- A LABEL IS A NEUTRAL SUBJECT, NOT A POSITION. Labels must name a noun-phrase subject area broad enough that every fact on the subject — including facts that take opposite positions on it — fits under it. They MUST NOT contain directional verbs, attitudinal adjectives, or polarity qualifiers. The same label must work whether the person is pro, anti, mixed, or evolving on the subject.
- ONE SUBJECT per label. NEVER glue multiple distinct domains together. ❌ "sécurité, justice et protection des mineurs" (three subjects) → split into the single subject the fact is actually about.
- FORBIDDEN label vocabulary (non-exhaustive — apply the spirit, not just the list):
  * Directional verbs / verbal nouns: "défense de", "défendre", "lutte contre", "opposition à", "soutien à", "attachement à", "engagement pour", "discipline", "protection of", "support for", "fight against", "rejet de", "promotion de"
  * Polarity prefixes: "pro-", "anti-"
  * Attitudinal adjectives in the label: "strict", "ambitieux", "sacré", "ferme", "rigoureux"
  * Stance descriptors: "posture sur X", "position concernant Y", "vision de Z"
- GOOD labels (neutral, single-subject containers):
  * "politique des dépenses publiques" (covers BOTH "defends hospitals" AND "voted for austerity cuts")
  * "services publics et hôpitaux"
  * "politique salariale des enseignants" (covers BOTH "promised raise" AND "voted against raise")
  * "souveraineté industrielle française" (covers tariffs, offshoring, artisanat)
  * "politique climatique" (covers renewables, fossil fuels, EV mandates, carbon tax)
  * "libertés civiles et surveillance numérique"
  * "droits LGBT et coparentalité"
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
- ⚠️ ABSOLUTELY NEVER quote a sentence, phrase, or specific verb the person said. Labels describe topics, not statements. Examples of FORBIDDEN labels:
  * ❌ "Donald Trump a posté « FREE LEPEN »" — quoting an action
  * ❌ "Marine Le Pen a affirmé qu'elle ne se présenterait pas..." — quoting a statement
  * ❌ "denied bracelet candidacy" — quoting a verb + claim
- ⚠️ Do NOT pack event-detail into the label. The label should NOT name a specific ruling, date, or instance.
  * ❌ "presidential candidacy eligibility and legal sentencing 2027 first-instance bracelet ruling"
  * ✅ "presidential candidacy 2027"
- ⚠️ NEVER use a META / PATTERN-OF-BEHAVIOR / RHETORICAL label. The label must name a SUBJECT, not a way of speaking. ❌ "policy announcements contradictions" / "campaign promises" / "denials and reversals" / "flip-flops on foreign policy". ✅ "US tariffs on Chinese goods" / "Strait of Hormuz coalition".`

const stanceRefreshLanguageAndFormat = `LANGUAGE RULES:
- Write "label" and "summary" in FRENCH, always — even when some or all listed facts are in English. Do NOT emit any translation here; English (and any other language) is produced separately by the translation phase.

Return ONLY valid JSON:
{"label": "subject-area label in French", "summary": "at most 2 sentences, about 300 characters, in French"}`

// formatRefreshNewFact renders one new fact for the incremental prompt,
// including the tensions the analyze verified for it.
func formatRefreshNewFact(f refreshNewFact) string {
	var b strings.Builder
	if f.Verb != "" {
		fmt.Fprintf(&b, "- (%s) %s\n", f.Verb, f.Text)
	} else {
		fmt.Fprintf(&b, "- %s\n", f.Text)
	}
	for _, c := range f.Contradicts {
		fmt.Fprintf(&b, "  ⚠ CONTRADICTS the earlier fact: « %s »", strings.ReplaceAll(c.EarlierFactText, "\n", " "))
		if c.Reason != "" {
			fmt.Fprintf(&b, " — %s", c.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// foldStanceMetadata is the incremental refresh call: given the current label +
// summary and the facts just added, it returns the updated label + summary.
// O(1) in the stance size. Empty label + summary means the stance has no
// summary yet and the facts describe it from scratch. Tensions are explicit
// input (the analyze already found them), so the summary never has to
// rediscover a contradiction among facts it cannot see.
func (sm *StanceManager) foldStanceMetadata(ctx context.Context, label, summary string, newFacts []refreshNewFact) (string, string, error) {
	if len(newFacts) == 0 {
		return label, summary, nil
	}
	var factsBuilder strings.Builder
	for _, f := range newFacts {
		factsBuilder.WriteString(formatRefreshNewFact(f))
	}

	current := "CURRENT LABEL: (none yet — this is the first summary of the stance)\nCURRENT SUMMARY: (none yet)"
	if strings.TrimSpace(label) != "" || strings.TrimSpace(summary) != "" {
		current = fmt.Sprintf("CURRENT LABEL: %s\nCURRENT SUMMARY: %s", label, summary)
	}

	prompt := fmt.Sprintf(`You maintain the topic label and summary of a stance document about a political figure. The stance already has a label and a summary covering its earlier facts (you do not see those facts — the summary stands for them). New facts were just added. Update the label and summary so they reflect the stance NOW. The current label and summary and the new facts are given after the rules.

`+stanceLabelRules+`
- KEEP the current label unless the new facts fall outside it; then broaden it to a neutral subject that covers both the old summary and the new facts. Never narrow it.

`+stanceSummaryRules+`
- REWRITE the whole summary from the current one and the new facts so that it stays within that length: the new facts change the summary only when they change the position or its direction; a new detail, figure or event is not added.

%s

NEW FACTS JUST ADDED (one "- " line per fact, with its verb in parentheses when known; an indented ⚠ line names an earlier fact the new fact was found to CONTRADICT):
%s

`+stanceRefreshLanguageAndFormat, current, factsBuilder.String())

	return sm.callRefreshLLM(ctx, prompt)
}

func (sm *StanceManager) callRefreshLLM(ctx context.Context, prompt string) (string, string, error) {
	content, err := sm.callJSONLLM(ctx, "You maintain stance metadata for political facts. Return only valid JSON.", prompt, 0.2, stanceRefreshOutputTokens, llm.TaskStanceLabel)
	if err != nil {
		return "", "", fmt.Errorf("metadata refresh: %w", err)
	}
	var resp stanceMetadataResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return "", "", fmt.Errorf("parsing refresh response: %w", err)
	}
	label, summary := strings.TrimSpace(string(resp.Label)), strings.TrimSpace(resp.Summary)
	return label, sm.shortenSummary(ctx, summary), nil
}

// shortenSummary sends a summary over stanceSummaryMaxRunes back once to be
// shortened to its position; a failed or still-long rewrite keeps the
// original (logged): a long summary is better than none.
func (sm *StanceManager) shortenSummary(ctx context.Context, summary string) string {
	if utf8.RuneCountInString(summary) <= stanceSummaryMaxRunes {
		return summary
	}
	prompt := `Shorten this summary of a politician's stance to AT MOST 2 sentences and about 300 characters, in French. Keep only the position and its direction; drop measures, figures, dates, places, events and any mention of tension or contrast. Do not add anything.

SUMMARY:
` + summary + `

Return ONLY valid JSON: {"summary": "the shortened summary"}`
	content, err := sm.callJSONLLM(ctx, "You shorten texts. Return only valid JSON.", prompt, 0.0, stanceRefreshOutputTokens, llm.TaskStanceLabel)
	if err != nil {
		log.Warn().Err(err).Int("runes", utf8.RuneCountInString(summary)).Msg("stance summary: shortening failed; the long summary is kept")
		return summary
	}
	var resp struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(content), &resp); err != nil || strings.TrimSpace(resp.Summary) == "" {
		log.Warn().Err(err).Msg("stance summary: shortening returned no summary; the long summary is kept")
		return summary
	}
	short := strings.TrimSpace(resp.Summary)
	if utf8.RuneCountInString(short) >= utf8.RuneCountInString(summary) {
		return summary
	}
	return short
}

// parseStanceLLMResponse parses the LLM's JSON response, handling optional markdown code blocks.
func parseStanceLLMResponse(content string) (*stanceLLMResponse, error) {
	content = llm.StripJSONCodeFence(strings.TrimSpace(content))

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

	prompt := mergeDecisionPrompt(personName, cluster)

	// Thinking is set by llm.TaskStanceMerge (llm/task.go).
	content, err := sm.callJSONLLM(ctx, "You are a political fact stance-merge classifier. Return only valid JSON.", prompt, 0.1, 800, llm.TaskStanceMerge)
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

// mergeDecisionPrompt is the exact user message MergeClusterDecision sends for
// personName and cluster. It is a pure function of its inputs so callers can
// hash it as the rulebook version a merge verdict was judged under.
//
// Layout is rules first, per-call data next, response format last: the rule
// block is byte-identical across calls, so vLLM prefix caching prefills it once
// per process instead of once per person (see CHANGES.md, prompt layout).
func mergeDecisionPrompt(personName string, cluster []StanceForMerge) string {
	var stancesBuilder strings.Builder
	// The lines carry no list position: a model given "[1] id=12491" answered
	// with the positions, which name no stance of the cluster.
	for _, s := range cluster {
		stancesBuilder.WriteString(fmt.Sprintf("- id=%d, label=%q, summary=%q\n",
			s.ID, s.Label, truncate(s.Summary, 400)))
	}

	return fmt.Sprintf(`You are deciding which subsets of the listed stance documents about one person should be merged because they cover the SAME NEUTRAL SUBJECT AREA. A stance is defined as a neutral subject container — both supportive and opposing facts on that subject belong inside it.

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

UNIFIED LABEL RULES (for the `+"`label`"+` field of each merge):
- The unified label MUST be a NEUTRAL SUBJECT, not a position. The same label must work for both pro and anti facts in the merged stance.
- FORBIDDEN label vocabulary: directional verbs ("défense de", "lutte contre", "opposition à", "soutien à", "engagement pour", "discipline"), polarity prefixes ("pro-", "anti-"), attitudinal adjectives ("strict", "sacré", "ferme").
- GOOD unified labels: "politique des dépenses publiques", "politique climatique", "services publics et hôpitaux", "souveraineté industrielle française", "Iran policy", "EU economic policy".
- BAD unified labels: ❌ "défense de la transition écologique" → ✅ "politique climatique". ❌ "engagement pour les services publics" → ✅ "services publics et hôpitaux".
- Length: 4-12 words. Max 120 chars.

UNIFIED SUMMARY RULES (for the `+"`summary`"+` field):
`+stanceSummaryRules+`

**LANGUAGE RULE:** Write the unified `+"`summary`"+` and `+"`label`"+` in the same language as the stances provided. If stances span multiple languages, use French.

PERSON: %s

STANCES IN THIS CANDIDATE CLUSTER:
%s

Return ONLY valid JSON. stance_ids are the id= values of the stances above:
{
  "reasoning": "1-3 sentences explaining your decision",
  "merges": [
    {"stance_ids": [12491, 12534], "summary": "unified 1-3 sentence summary", "label": "specific subject label"}
  ]
}

If nothing should be merged, return {"reasoning": "...", "merges": []}.
Stances not listed in any merge group will be left as-is.`,
		personName, stancesBuilder.String())
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
