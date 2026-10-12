package core

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// InsertedFactMeta holds metadata about a successfully inserted fact.
// Used by batch processing to chain classification after fact extraction.
type InsertedFactMeta struct {
	FactID   int64
	PersonID int64 // Resolved subject entity person ID
}

// InsertResult contains the outcome of a batch InsertFacts call.
type InsertResult struct {
	Inserted     int                // Number of facts successfully inserted
	Skipped      int                // Number of facts skipped: subject rejected by the entity validator, or web source without a quote
	InsertedMeta []InsertedFactMeta // Metadata for each successfully inserted fact
}

// StanceProcessor routes a newly inserted fact to its (existing or new) stance.
// The production implementation is *StanceManager (LLM-backed); tests may
// supply a stub that satisfies the invariant without an LLM round-trip.
type StanceProcessor interface {
	ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error)
}

// FactInserter handles fact insertion with mandatory stance-based processing.
// Every inserted fact is routed through StanceProcessor.ProcessNewFact, which
// classifies it against existing stances (DUPLICATE / NEW_INFO / CONTRADICTION /
// NEW_STANCE) and updates the per-person stance documents accordingly. This is
// the single ingestion path: there is no fallback, no skip flag, and no other
// way to land a fact in the database.
type FactInserter struct {
	storer          Storer
	stanceProcessor StanceProcessor
	groupStorer     GroupStorer         // Set by EnableDedupe; stores the group a same-claim fact joins
	dedupeJudge     SameOccurrenceJudge // Set by EnableDedupe; the mandatory last decider on grouping
}

// dedupeEmbeddingFloor is the cosine similarity at or above which an existing
// fact is a dedupe candidate handed to the LLM gate. 0.70 catches genuine
// restatements that embed apart (e.g. the Retailleau "candidature jusqu'au
// bout" pair at 0.78) while staying high enough to keep the per-run LLM call
// volume bounded.
const dedupeEmbeddingFloor = 0.70

// dedupeTextSimilarityFloor is the trigram similarity (pg_trgm) of the fact
// texts at or above which an existing fact is a dedupe candidate regardless of
// embedding distance. Measured on prod pairs (2026-09-17): restatements of one
// statement score 0.38-0.84, distinct same-topic claims 0.24-0.40. The LLM gate
// decides, so a borderline candidate costs one call, not a wrong merge.
const dedupeTextSimilarityFloor = 0.40

// Storer defines the interface for storage operations needed by FactInserter.
// This is typically implemented by storage.PostgresStorer.
type Storer interface {
	// FindOrInsertPersons resolves person names to person IDs, creating new records if needed.
	FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error)

	// FindOrInsertPersonWithType resolves a person name to a person ID with entity type.
	FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error)

	// FindSimilarActiveFacts returns the active facts that match q, closest
	// first (see SimilarFactQuery).
	FindSimilarActiveFacts(ctx context.Context, q SimilarFactQuery) ([]SimilarFact, error)

	// InsertFact inserts a new fact into the database.
	InsertFact(ctx context.Context, input StoreFactInput) (factID int64, err error)

	// DeleteFact removes a fact and its dependent rows. Used as a compensating
	// action when stance processing fails after the fact was already written.
	DeleteFact(ctx context.Context, factID int64) error

	// LinkSourceToFact links a source URL to a fact, creating a fact_sources record.
	LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (sourceID int64, err error)

	// LinkPersonsToSourceMention links persons to a fact source, creating fact_mentions records.
	LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error
}

// GroupStorer is the storage surface for claim grouping (see
// storage.MergeFactIntoGroup). Implemented by storage.PostgresStorer. Kept
// separate from Storer so test fakes and callers that don't group stay
// untouched.
type GroupStorer interface {
	ClaimEvidenceStorer
	// MostConcreteFact returns, among factIDs, the fact with the most
	// concrete text: the one a group shows as its parent.
	MostConcreteFact(ctx context.Context, factIDs []int64) (int64, error)
	// MergeFactIntoGroup makes from (and its members) members of into's group.
	MergeFactIntoGroup(ctx context.Context, from, into int64) error
	RefreshPersonScore(ctx context.Context, personID int64) error
}

// StoreFactInput encapsulates the data needed to store a fact and its source.
type StoreFactInput struct {
	SubjectEntity        string    // Primary entity this fact is about (resolved to person ID for linking)
	SubjectEntityType    string    // Entity type: "human", "party", or "organization" (used when creating new persons)
	NormalizedFactText   string    // Native-language fact text with names (for display and LLM comparison)
	NormalizedFactTextEn string    // English translation; empty when source is already English
	EmbeddingText        string    // Name-free version used ONLY for embedding generation
	Embedding            []float32 // Vector embedding (generated from EmbeddingText)
	OriginalQuote        string
	CanonicalVerb        string
	InvolvedPeople       []string // Secondary people mentioned in the fact (not the subject)
	SourceURL            string
	PublishedAt          *time.Time
	SourceType           string
	Metadata             map[string]any
	Lang                 string // BCP-47 language code ("fr", "en", ...); REQUIRED on all new inserts.
}

// SimilarFactQuery selects the grouping candidates of a new fact: the active
// facts of PersonIDs within MaxDistance (cosine) of Embedding OR whose text
// trigram-matches Text at least TextFloor. When Evidence is set, a candidate
// must also meet the evidence rule against the new fact's source (Quote,
// PublishedAt).
type SimilarFactQuery struct {
	PersonIDs   []int64
	Embedding   []float32
	MaxDistance float64
	Text        string
	TextFloor   float64
	Evidence    *GroupEvidence
	Quote       string
	PublishedAt *time.Time
	Limit       int
}

// SimilarFact represents a fact that is similar to a given embedding.
type SimilarFact struct {
	ID            int64   // Fact ID
	Text          string  // Fact text
	Distance      float64 // Cosine distance (1 - cosine_similarity) from query embedding
	CanonicalVerb string  // Canonical verb of the fact ("" when unset)
}

// NewFactInserter creates a new FactInserter. The StanceProcessor is REQUIRED:
// every fact must be routed through stance processing so that the
// "every fact has a stance" invariant holds for the whole codebase. Callers
// without a StanceProcessor cannot insert facts — this is intentional.
func NewFactInserter(storer Storer, stance StanceProcessor) *FactInserter {
	if storer == nil {
		panic("NewFactInserter: storer is nil")
	}
	if stance == nil {
		panic("NewFactInserter: stance processor is nil — every fact must be routed through a stance, no exceptions")
	}

	return &FactInserter{
		storer:          storer,
		stanceProcessor: stance,
	}
}

// EnableDedupe turns on claim grouping: an incoming fact that reports an
// occurrence one of the subject's active facts already reports (see
// claim_evidence.go) is stored (text, source, quote all preserved) and
// grouped with it; the group's parent is whichever of the two is more
// concrete. Only the parent is active; every contradiction judge sees the
// members alongside it. Embedding and text similarity only nominate
// candidates, the gate always decides. If the
// OPEN_AI_ env is unconfigured it logs and leaves dedupe off: every fact is
// inserted standalone. Production ingestion paths call this; tests that
// don't, keep the deterministic no-dedupe behavior.
func (fi *FactInserter) EnableDedupe(groups GroupStorer) {
	if groups == nil {
		panic("EnableDedupe: group storer is nil")
	}
	judge, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Fact dedupe disabled (OPEN_AI env not configured); every fact is inserted standalone")
		return
	}
	fi.dedupeJudge = judge
	fi.groupStorer = groups
}

// InsertFacts processes a batch of facts. Each fact is inserted, then either
// grouped with the same-claim fact it restates (when dedupe is enabled) or
// routed through the stance manager. If stance processing fails for any fact, the fact (and its source/
// mention rows) is deleted and the batch fails — there is no silent fallback.
func (fi *FactInserter) InsertFacts(ctx context.Context, facts []StoreFactInput) (*InsertResult, error) {
	result := &InsertResult{}
	for i, fact := range facts {
		log.Debug().
			Int("index", i+1).
			Int("total", len(facts)).
			Str("fact_text", util.TruncateString(fact.NormalizedFactText, 100)).
			Msg("Processing fact for insertion")

		factID, personID, err := fi.insertSingleFact(ctx, fact)
		if err != nil {
			// Entity-validator rejection means the subject isn't a real entity —
			// not a pipeline failure, just a per-fact skip. Don't abort the batch.
			if IsEntityRejected(err) {
				log.Info().
					Err(err).
					Str("subject", fact.SubjectEntity).
					Str("subject_type", fact.SubjectEntityType).
					Str("fact_text", util.TruncateString(fact.NormalizedFactText, 100)).
					Msg("Skipping fact: subject entity rejected by validator")
				result.Skipped++
				continue
			}
			// Web-extracted fact with no source quote: unverifiable, skip it
			// without aborting the batch (mirrors the entity-rejected skip above).
			if isQuoteMissing(err) {
				log.Warn().
					Str("source_type", fact.SourceType).
					Str("source_url", fact.SourceURL).
					Str("subject", fact.SubjectEntity).
					Str("fact_text", util.TruncateString(fact.NormalizedFactText, 100)).
					Msg("Skipping fact: web-extracted source has empty original_quote (unverifiable)")
				result.Skipped++
				continue
			}
			return result, fmt.Errorf("failed to insert fact %d/%d: %w", i+1, len(facts), err)
		}
		result.Inserted++
		result.InsertedMeta = append(result.InsertedMeta, InsertedFactMeta{
			FactID:   factID,
			PersonID: personID,
		})
	}
	return result, nil
}

// insertSingleFact handles insertion of a single fact and its mandatory stance
// routing. Returns (factID, personID, error).
func (fi *FactInserter) insertSingleFact(ctx context.Context, input StoreFactInput) (int64, int64, error) {
	if input.SubjectEntity == "" {
		return 0, 0, fmt.Errorf("subject_entity is required")
	}

	// Web/news-extracted facts must carry a verbatim source quote. An empty quote
	// means the extractor produced an unverifiable claim (a hallucination with no
	// snippet behind it), so reject it rather than store a fact nothing backs.
	// Scoped to LLM-extracted web sources; structured sources (e.g. vote records)
	// always populate the quote from metadata and are intentionally excluded.
	if quoteMissing(input) {
		return 0, 0, errMissingQuote
	}

	// Resolve subject entity to person ID
	var subjectID int64
	var err error
	if input.SubjectEntityType != "" {
		subjectID, err = fi.storer.FindOrInsertPersonWithType(ctx, input.SubjectEntity, input.SubjectEntityType)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to find/insert subject entity with type: %w", err)
		}
	} else {
		subjectIDs, err := fi.storer.FindOrInsertPersons(ctx, []string{input.SubjectEntity})
		if err != nil {
			return 0, 0, fmt.Errorf("failed to find/insert subject entity: %w", err)
		}
		if len(subjectIDs) == 0 {
			return 0, 0, fmt.Errorf("failed to resolve subject entity: %s", input.SubjectEntity)
		}
		subjectID = subjectIDs[0]
	}

	// Same-occurrence check before inserting: embedding distance and text
	// similarity nominate the subject's active facts as candidates, storage
	// keeps the ones that meet the evidence rule, and the LLM gate is the
	// mandatory last decider (see claim_evidence.go). A confirmed hit does
	// not drop the fact: groupTarget records the matched fact and the new
	// one joins its group after insertion. Only a free-text fact is grouped:
	// a vote is its own record.
	var groupTarget *SimilarFact
	groupVerdict := OccurrenceDifferent
	if len(input.Embedding) > 0 && fi.dedupeEnabled() && isWebExtractedSource(input.SourceType) {
		target, verdict, err := fi.findGroupTarget(ctx, subjectID, input)
		if err != nil {
			return 0, 0, err
		}
		groupTarget, groupVerdict = target, verdict
	}

	// Insert the fact
	factID, err := fi.storer.InsertFact(ctx, input)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to insert fact: %w", err)
	}
	metrics.IncrementFactsExtracted(input.SourceType, true)

	// Link source to the fact
	sourceID, err := fi.storer.LinkSourceToFact(
		ctx,
		factID,
		input.SourceURL,
		input.OriginalQuote,
		input.PublishedAt,
		input.SourceType,
		input.Metadata,
	)
	if err != nil {
		fi.compensateDelete(ctx, factID, "linking source")
		return 0, 0, fmt.Errorf("failed to link source: %w", err)
	}

	if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, []int64{subjectID}); err != nil {
		fi.compensateDelete(ctx, factID, "linking subject entity")
		return 0, 0, fmt.Errorf("failed to link subject entity: %w", err)
	}

	// Fold path: the fact is a same-claim restatement — group it with the fact
	// it restates instead of running stance analysis (the group's parent holds
	// the stance slot and the contradictions; analyzing a restatement would
	// only duplicate LLM calls). A new fact promoted to parent takes over the
	// matched fact's stance slot. On any fold error we fall through to normal
	// stance processing (fail-open).
	if groupTarget != nil {
		if foldErr := fi.foldNewFact(ctx, subjectID, factID, input, *groupTarget, groupVerdict); foldErr != nil {
			log.Warn().Err(foldErr).Int64("fact_id", factID).Int64("matched_fact_id", groupTarget.ID).
				Msg("Grouping failed; falling back to normal stance routing")
		} else {
			return factID, subjectID, nil
		}
	}

	// Mandatory stance processing. Failure rolls back the fact via compensating
	// delete so that the "every fact has a stance" invariant holds — unless the
	// fact is already in its stance and only the label refresh is pending.
	stanceResult, err := fi.stanceProcessor.ProcessNewFact(ctx, subjectID, factID, input.NormalizedFactText, input.Embedding, input.CanonicalVerb)
	if err != nil {
		if !errors.Is(err, ErrStanceRefreshPending) {
			fi.compensateDelete(ctx, factID, "stance processing")
		}
		return 0, 0, fmt.Errorf("stance processing failed for fact %d: %w", factID, err)
	}

	log.Info().
		Int64("fact_id", factID).
		Int64("source_id", sourceID).
		Str("subject_entity", input.SubjectEntity).
		Str("verdict", stanceResult.Verdict).
		Int64("stance_id", stanceResult.StanceID).
		Msg("Inserted fact and routed through stance")

	return factID, subjectID, nil
}

// findGroupTarget returns the active fact the incoming fact reports again,
// with the gate's verdict, or nil. An unavailable LLM is an error: grouping
// is decided once, at insert time, so inserting the fact ungrouped would make
// the outage's answer final.
func (fi *FactInserter) findGroupTarget(ctx context.Context, subjectID int64, input StoreFactInput) (*SimilarFact, OccurrenceVerdict, error) {
	rule, err := groupEvidenceRule()
	if err != nil {
		return nil, OccurrenceDifferent, err
	}
	similar, err := fi.storer.FindSimilarActiveFacts(ctx, SimilarFactQuery{
		PersonIDs:   []int64{subjectID},
		Embedding:   input.Embedding,
		MaxDistance: 1.0 - dedupeEmbeddingFloor,
		Text:        input.NormalizedFactText,
		TextFloor:   dedupeTextSimilarityFloor,
		Evidence:    &rule,
		Quote:       input.OriginalQuote,
		PublishedAt: input.PublishedAt,
		Limit:       5,
	})
	if err != nil {
		log.Warn().Err(err).Str("fact", util.TruncateString(input.NormalizedFactText, 80)).
			Msg("Dedup check failed, proceeding with insertion")
		return nil, OccurrenceDifferent, nil
	}
	if len(similar) == 0 {
		metrics.RecordDecision(metrics.StageFactDedupe, metrics.OutcomeNoCandidates)
		return nil, OccurrenceDifferent, nil
	}
	ids := make([]int64, len(similar))
	for i, sf := range similar {
		ids[i] = sf.ID
	}
	sources, err := fi.groupStorer.LoadClaimSources(ctx, ids, rule.SourceTypes)
	if err != nil {
		log.Warn().Err(err).Str("fact", util.TruncateString(input.NormalizedFactText, 80)).
			Msg("Loading candidate sources failed, proceeding with insertion")
		return nil, OccurrenceDifferent, nil
	}
	incoming := ClaimEvidence{Text: input.NormalizedFactText, Sources: []ClaimSource{{
		Outlet: OutletOf(input.SourceURL), Quote: input.OriginalQuote, PublishedAt: input.PublishedAt,
	}}}
	for _, sf := range similar {
		existing := ClaimEvidence{Text: sf.Text, Sources: sources[sf.ID]}
		verdict, jErr := fi.askDedupeGate(ctx, existing, incoming)
		if errors.Is(jErr, llm.ErrUnavailable) {
			return nil, OccurrenceDifferent, fmt.Errorf("fact-dedupe gate: %w", jErr)
		}
		if jErr != nil {
			log.Warn().Err(jErr).Int64("existing_fact_id", sf.ID).
				Msg("LLM fact-dedupe check failed twice, proceeding with insertion")
			metrics.RecordDecision(metrics.StageFactDedupe, metrics.OutcomeGateError)
			continue
		}
		if verdict.Groupable() {
			log.Info().
				Int64("existing_fact_id", sf.ID).
				Float64("distance", sf.Distance).
				Bool("contained", verdict == OccurrenceContained).
				Str("fact", util.TruncateString(input.NormalizedFactText, 80)).
				Msg("Same-occurrence fact (LLM gate)")
			metrics.RecordDecision(metrics.StageFactDedupe, metrics.OutcomeFolded)
			sfCopy := sf
			return &sfCopy, verdict, nil
		}
	}
	metrics.RecordDecision(metrics.StageFactDedupe, metrics.OutcomeDistinct)
	return nil, OccurrenceDifferent, nil
}

// askDedupeGate asks the judge whether existing and the incoming fact report
// one occurrence. A failed call is retried once: grouping is decided exactly
// once, at insert time, so a transient LLM failure would otherwise be a
// permanent miss.
func (fi *FactInserter) askDedupeGate(ctx context.Context, existing, incoming ClaimEvidence) (OccurrenceVerdict, error) {
	verdict, err := fi.dedupeJudge.IsSameOccurrence(ctx, existing, incoming)
	if err == nil {
		return verdict, nil
	}
	log.Warn().Err(err).Msg("LLM fact-dedupe check failed, retrying once")
	return fi.dedupeJudge.IsSameOccurrence(ctx, existing, incoming)
}

func (fi *FactInserter) dedupeEnabled() bool {
	return fi.dedupeJudge != nil && fi.groupStorer != nil
}

// foldNewFact puts a freshly inserted fact in one group with the active fact
// it reports again. The more concrete of the two is the group's parent: when
// the new wording carries more detail it is promoted and the matched fact
// (with any members) moves under it. For a contained pair the fuller fact is
// the parent, so that no claim is hidden.
func (fi *FactInserter) foldNewFact(ctx context.Context, personID, newFactID int64, input StoreFactInput, matched SimilarFact, verdict OccurrenceVerdict) error {
	gs := fi.groupStorer
	var parent int64
	if verdict == OccurrenceContained {
		parent = []int64{matched.ID, newFactID}[fullerFact(matched.Text, input.NormalizedFactText)]
	} else {
		var err error
		parent, err = gs.MostConcreteFact(ctx, []int64{matched.ID, newFactID})
		if err != nil {
			return err
		}
	}
	from, into := newFactID, matched.ID
	if parent == newFactID {
		from, into = matched.ID, newFactID
	}
	if err := gs.MergeFactIntoGroup(ctx, from, into); err != nil {
		return fmt.Errorf("grouping fact %d with %d: %w", newFactID, matched.ID, err)
	}
	if err := gs.RefreshPersonScore(ctx, personID); err != nil {
		log.Warn().Err(err).Int64("person_id", personID).Msg("Failed to refresh fact count after grouping")
	}
	log.Info().
		Int64("fact_id", newFactID).
		Int64("matched_fact_id", matched.ID).
		Int64("parent_fact_id", into).
		Str("fact", util.TruncateString(input.NormalizedFactText, 80)).
		Msg("Grouped one-occurrence fact")
	return nil
}

// compensateDelete rolls back a partially inserted fact. Errors here are logged
// but cannot be propagated (we are already in the error path). A separate audit
// metric flags any orphan facts that slip through. The rollback ignores the
// cancellation of ctx: a run cut under a fact's stance routing must not leave
// the fact stored, or a resume would count it as done.
func (fi *FactInserter) compensateDelete(ctx context.Context, factID int64, stage string) {
	if delErr := fi.storer.DeleteFact(context.WithoutCancel(ctx), factID); delErr != nil {
		log.Error().
			Err(delErr).
			Int64("fact_id", factID).
			Str("stage", stage).
			Msg("Compensating delete failed — fact may be orphaned without a stance")
	} else {
		log.Warn().
			Int64("fact_id", factID).
			Str("stage", stage).
			Msg("Rolled back fact after failure during ingestion")
	}
}
