package core

import (
	"context"
	"digital-mirror/backend/metrics"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// FactRelationInput represents a relation between two facts.
// This type is defined in core to avoid import cycles between core and storage.
type FactRelationInput struct {
	GeneralFactID  int64
	SpecificFactID int64
	RelationType   string // "duplicate" or "implication"
	Reason         string
}

// InsertedFactMeta holds metadata about a successfully inserted fact.
// Used by batch processing to chain classification after fact extraction.
type InsertedFactMeta struct {
	FactID   int64
	PersonID int64 // Resolved subject entity person ID
}

// InsertResult contains the outcome of a batch InsertFacts call.
type InsertResult struct {
	Inserted     int                // Number of facts successfully inserted
	Skipped      int                // Number of facts skipped as duplicates
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
	storer              Storer
	stanceProcessor     StanceProcessor
	autoDedupeThreshold float64         // Cosine similarity at/above which to auto-skip as duplicate (verb-aware)
	llmDedupeFloor      float64         // Lower bound of the LLM "gray zone"; below this similarity, never dedup
	metaStorer          MetaFoldStorer  // Non-nil (with metaSynth) switches dedupe from drop to fold-under-meta
	metaSynth           MetaSynthesizer // LLM that (re)writes the meta-fact text
	dedupeJudge         factDedupeJudge // Optional; when set, the [floor, auto) band (and similar-but-different-verb pairs) are LLM-adjudicated
}

// factDedupeJudge adjudicates whether two same-subject statements are the same
// claim (one a redundant restatement of the other). Implemented by
// *FactDedupeAdjudicator. Optional: when nil, the gray-zone band is not consulted
// and only the >= autoDedupeThreshold verb-aware auto-dedup runs.
type factDedupeJudge interface {
	IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error)
}

// defaultLLMDedupeFloor is the lower bound of the similarity band handed to the
// LLM adjudicator. 0.70 catches genuine restatements that slip just under the
// 0.80 auto-dedup line (e.g. the Retailleau "candidature jusqu'au bout" pair at
// 0.78) while staying high enough to keep the per-run LLM call volume bounded.
const defaultLLMDedupeFloor = 0.70

// Storer defines the interface for storage operations needed by FactInserter.
// This is typically implemented by storage.PostgresStorer.
type Storer interface {
	// FindOrInsertPersons resolves person names to person IDs, creating new records if needed.
	FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error)

	// FindOrInsertPersonWithType resolves a person name to a person ID with entity type.
	FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error)

	// FindSimilarActiveFacts searches for active (non-duplicate) facts similar to the given embedding.
	FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, limit int) ([]SimilarFact, error)

	// InsertFact inserts a new fact into the database.
	InsertFact(ctx context.Context, input StoreFactInput) (factID int64, err error)

	// DeleteFact removes a fact and its dependent rows. Used as a compensating
	// action when stance processing fails after the fact was already written.
	DeleteFact(ctx context.Context, factID int64) error

	// LinkSourceToFact links a source URL to a fact, creating a fact_sources record.
	LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (sourceID int64, err error)

	// LinkPersonsToSourceMention links persons to a fact source, creating fact_mentions records.
	LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error

	// GetFactCanonicalVerb returns the canonical verb for a given fact ID.
	GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error)

	// InsertFactRelationsBatch inserts duplicate/implication relations. Still used
	// by the OpenAI-batch classification flow in batch_job.go; that path is now
	// redundant with stance-based clustering and queued for removal.
	InsertFactRelationsBatch(ctx context.Context, inputs []FactRelationInput) error
}

// MetaFoldStorer is the storage surface for meta-fact folding. Implemented by
// storage.PostgresStorer. Kept separate from Storer so test fakes and callers
// that don't fold stay untouched.
type MetaFoldStorer interface {
	GetMetaForFact(ctx context.Context, factID int64) (int64, bool, error)
	CreateMetaFact(ctx context.Context, personID int64, text, embeddingText string, memberEmbedding []float32, lang, canonicalVerb string) (int64, error)
	UpdateMetaFactText(ctx context.Context, metaID int64, text string) error
	GetMetaMemberTexts(ctx context.Context, metaID int64) ([]string, error)
	MarkFactAsDuplicate(ctx context.Context, duplicateID int64, canonicalID int64) error
	RepointFactDuplicate(ctx context.Context, general, specific int64) (int, int, error)
	GetFactStanceID(ctx context.Context, personID int64, factID int64) (int64, bool, error)
	AddFactToStance(ctx context.Context, stanceID int64, factID int64) error
	RefreshPersonFactCount(ctx context.Context, personID int64) error
	CountFactMentionPersons(ctx context.Context, factID int64) (int, error)
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
	// CoActorIDs are pre-resolved person ids who PERFORMED the fact's action
	// alongside the subject (e.g. every deputy/party casting the same vote on a
	// law, collapsed into one shared fact row). Unlike InvolvedPeople — who are
	// bystanders and explicitly do NOT own the fact — co-actors get their own
	// fact_mentions row, i.e. full attribution. Consequences, deliberately:
	//   - contradiction detection (mention-scoped GetFactsForPerson) pairs the
	//     shared fact against each co-actor's other facts, so "said X, voted Y"
	//     can fire for every voter, not just the arbitrary first record;
	//   - each co-actor's fact_count (and honesty-score denominator, computed
	//     live from mentions) includes the shared fact;
	//   - stance routing still runs ONLY for the subject: stances are
	//     per-person documents, so co-actors' stance systems never see this
	//     fact — their coverage comes from the nightly pairwise detector.
	CoActorIDs  []int64
	SourceURL   string
	PublishedAt *time.Time
	SourceType  string
	Metadata    map[string]any
	Lang        string // BCP-47 language code ("fr", "en", ...); REQUIRED on all new inserts.
}

// SimilarFact represents a fact that is similar to a given embedding.
type SimilarFact struct {
	ID       int64   // Fact ID
	Text     string  // Fact text
	Distance float64 // Cosine distance (1 - cosine_similarity) from query embedding
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

	// 0.80 (was 0.85): the embedding_text is verb-free and strips dates/numbers/names,
	// so same-topic facts should embed very close. Dropping the threshold catches
	// near-duplicate news reports of the same event that differ only in incidental
	// details (an extra named player, a date string). Verb-aware: opposing positions
	// never dedup regardless of similarity, so lowering is safe.
	autoDedupeThreshold := getEnvFloat("DEDUPE_AUTO_THRESHOLD", 0.80)

	return &FactInserter{
		storer:              storer,
		stanceProcessor:     stance,
		autoDedupeThreshold: autoDedupeThreshold,
		llmDedupeFloor:      defaultLLMDedupeFloor,
	}
}

// EnableLLMDedupe activates LLM adjudication of the gray-zone similarity band
// ([llmDedupeFloor, autoDedupeThreshold)) and of similar-but-different-verb
// pairs that the cheap verb heuristic leaves undecided. Best-effort: if the
// OPEN_AI_ env is unconfigured it logs and leaves band-dedup off, so callers
// keep working with the embedding-only path. Production ingestion paths call
// this; tests that don't, retain the deterministic embedding-only behavior.
// EnableMetaFolding switches the dedupe path from DROPPING a same-claim fact to
// KEEPING it as a member of a meta-fact: the new fact is stored (text, source,
// quote all preserved), linked as a 'duplicate' member of an LLM-synthesized
// meta that alone participates in contradiction pairing. The meta's text is
// re-synthesized on every absorbed member and its pair checks re-open.
func (fi *FactInserter) EnableMetaFolding(storer MetaFoldStorer, synth MetaSynthesizer) {
	fi.metaStorer = storer
	fi.metaSynth = synth
	log.Info().Msg("Meta-fact folding ENABLED for fact insertion (same-claim facts are kept and folded, not dropped)")
}

func (fi *FactInserter) EnableLLMDedupe() {
	judge, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("LLM fact-dedupe disabled (OPEN_AI env not configured); using embedding-only dedup")
		return
	}
	fi.dedupeJudge = judge
}

// InsertFacts processes a batch of facts. Each fact goes through verb-aware
// near-duplicate dedup, then is inserted, then is routed through the stance
// manager. If stance processing fails for any fact, the fact (and its source/
// mention rows) is deleted and the batch fails — there is no silent fallback.
func (fi *FactInserter) InsertFacts(ctx context.Context, facts []StoreFactInput) (*InsertResult, error) {
	result := &InsertResult{}
	for i, fact := range facts {
		log.Debug().
			Int("index", i+1).
			Int("total", len(facts)).
			Str("fact_text", truncate(fact.NormalizedFactText, 100)).
			Msg("Processing fact for insertion")

		factID, personID, inserted, err := fi.insertSingleFact(ctx, fact)
		if err != nil {
			// Entity-validator rejection means the subject isn't a real entity —
			// not a pipeline failure, just a per-fact skip. Don't abort the batch.
			if isEntityRejected(err) {
				log.Info().
					Err(err).
					Str("subject", fact.SubjectEntity).
					Str("subject_type", fact.SubjectEntityType).
					Str("fact_text", truncate(fact.NormalizedFactText, 100)).
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
					Str("fact_text", truncate(fact.NormalizedFactText, 100)).
					Msg("Skipping fact: web-extracted source has empty original_quote (unverifiable)")
				result.Skipped++
				continue
			}
			return result, fmt.Errorf("failed to insert fact %d/%d: %w", i+1, len(facts), err)
		}
		if inserted {
			result.Inserted++
			result.InsertedMeta = append(result.InsertedMeta, InsertedFactMeta{
				FactID:   factID,
				PersonID: personID,
			})
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

// insertSingleFact handles insertion of a single fact and its mandatory stance
// routing. Returns (factID, personID, inserted, error). factID/personID are 0
// when the fact was skipped as a near-duplicate.
func (fi *FactInserter) insertSingleFact(ctx context.Context, input StoreFactInput) (int64, int64, bool, error) {
	if input.SubjectEntity == "" {
		return 0, 0, false, fmt.Errorf("subject_entity is required")
	}

	// Web/news-extracted facts must carry a verbatim source quote. An empty quote
	// means the extractor produced an unverifiable claim (a hallucination with no
	// snippet behind it), so reject it rather than store a fact nothing backs.
	// Scoped to LLM-extracted web sources; structured sources (e.g. vote records)
	// always populate the quote from metadata and are intentionally excluded.
	if quoteMissing(input) {
		return 0, 0, false, errMissingQuote
	}

	// Resolve subject entity to person ID
	var subjectID int64
	var err error
	if input.SubjectEntityType != "" {
		subjectID, err = fi.storer.FindOrInsertPersonWithType(ctx, input.SubjectEntity, input.SubjectEntityType)
		if err != nil {
			return 0, 0, false, fmt.Errorf("failed to find/insert subject entity with type: %w", err)
		}
	} else {
		subjectIDs, err := fi.storer.FindOrInsertPersons(ctx, []string{input.SubjectEntity})
		if err != nil {
			return 0, 0, false, fmt.Errorf("failed to find/insert subject entity: %w", err)
		}
		if len(subjectIDs) == 0 {
			return 0, 0, false, fmt.Errorf("failed to resolve subject entity: %s", input.SubjectEntity)
		}
		subjectID = subjectIDs[0]
	}

	// Near-duplicate check before inserting, in two tiers:
	//   1. High similarity (>= autoDedupeThreshold) + compatible verb (same verb
	//      or both external) → cheap auto-skip, no LLM. Different direct-claim
	//      verbs do NOT auto-dedup here, to preserve opposing stances.
	//   2. Gray zone [llmDedupeFloor, autoDedupeThreshold), OR similar-but-
	//      different-verb pairs: cosine + verb heuristics can't decide, so ask the
	//      LLM "same claim?" when an adjudicator is configured. This catches
	//      genuine restatements with different verbs (e.g. PROMISED vs CLAIMED of
	//      the same thing) that sit just under the auto line.
	// When folding is enabled, a same-claim hit no longer drops the fact —
	// foldTarget records the matched fact and the new one is stored as a member
	// of its meta after insertion.
	var foldTarget *SimilarFact
	if len(input.Embedding) > 0 {
		// Only widen the candidate search to the gray-zone floor when we can
		// actually adjudicate it; otherwise keep the original narrow search.
		searchDistance := 1.0 - fi.autoDedupeThreshold
		if fi.dedupeJudge != nil {
			searchDistance = 1.0 - fi.llmDedupeFloor
		}
		similar, err := fi.storer.FindSimilarActiveFacts(ctx, []int64{subjectID}, input.Embedding, searchDistance, 5)
		if err != nil {
			log.Warn().Err(err).Str("fact", truncate(input.NormalizedFactText, 80)).
				Msg("Dedup check failed, proceeding with insertion")
		} else {
			autoDistance := 1.0 - fi.autoDedupeThreshold
			for _, sf := range similar {
				existingVerb, verbErr := fi.storer.GetFactCanonicalVerb(ctx, sf.ID)
				if verbErr != nil {
					log.Debug().Err(verbErr).Int64("fact_id", sf.ID).Msg("Failed to get canonical verb for similar fact")
					continue
				}

				// Tier 1: cheap verb-aware auto-dedup for very-high-similarity pairs.
				// Only in DROP mode: when folding, the LLM gate below is the
				// mandatory last decider on membership — embeddings (and verbs)
				// only select candidates, they never fold on their own, because
				// the stance-blind embedding_text puts same-topic-but-different
				// claims well above 0.80 too.
				if sf.Distance <= autoDistance && !fi.foldingEnabled() {
					bothExternal := !isDirectClaimVerb(existingVerb) && !isDirectClaimVerb(input.CanonicalVerb)
					if existingVerb == input.CanonicalVerb || bothExternal {
						log.Info().
							Int64("existing_fact_id", sf.ID).
							Float64("distance", sf.Distance).
							Str("new_verb", input.CanonicalVerb).
							Str("existing_verb", existingVerb).
							Bool("both_external", bothExternal).
							Str("fact", truncate(input.NormalizedFactText, 80)).
							Msg("Skipping duplicate fact (same verb or both external + high similarity)")
						return 0, 0, false, nil
					}
				}

				// Tier 2: LLM gray-zone adjudication (and similar-but-different-verb).
				// In fold mode the broader position-level gate applies when the
				// judge offers it — folding keeps the raw fact, so grouping
				// same-position restatements is safe where dropping was not.
				// In fold mode a new fact with co-actors can never fold (see the
				// shared-fact guard below), so skip the judge calls entirely.
				if fi.dedupeJudge != nil && !(fi.foldingEnabled() && len(input.CoActorIDs) > 0) {
					var same bool
					var jErr error
					type positionGate interface {
						IsSamePosition(ctx context.Context, subject, factA, factB string) (bool, error)
					}
					if pg, ok := fi.dedupeJudge.(positionGate); ok && fi.foldingEnabled() {
						same, jErr = pg.IsSamePosition(ctx, input.SubjectEntity, sf.Text, input.NormalizedFactText)
					} else {
						same, jErr = fi.dedupeJudge.IsSameClaim(ctx, input.SubjectEntity, sf.Text, input.NormalizedFactText)
					}
					if jErr != nil {
						log.Warn().Err(jErr).Int64("existing_fact_id", sf.ID).
							Msg("LLM fact-dedupe check failed, proceeding with insertion")
						continue
					}
					if same {
						log.Info().
							Int64("existing_fact_id", sf.ID).
							Float64("distance", sf.Distance).
							Str("new_verb", input.CanonicalVerb).
							Str("existing_verb", existingVerb).
							Bool("folding", fi.foldingEnabled()).
							Str("fact", truncate(input.NormalizedFactText, 80)).
							Msg("Same-claim fact (LLM gray-zone adjudication)")
						if fi.foldingEnabled() {
							// Never fold a shared (co-actor) fact: folding marks the
							// member fact 'duplicate' GLOBALLY, so a shared vote fact
							// matched by one subject's news claim would vanish from
							// every other voter's active-fact set and stance. Shared
							// facts stay standalone; the pair is left to the
							// contradiction detector instead. The new fact's own side
							// (CoActorIDs) is guarded before the judge call above.
							if mentions, mErr := fi.metaStorer.CountFactMentionPersons(ctx, sf.ID); mErr != nil {
								log.Warn().Err(mErr).Int64("existing_fact_id", sf.ID).
									Msg("Fold mention-count check failed; keeping fact standalone")
								continue
							} else if mentions > 1 {
								log.Info().Int64("existing_fact_id", sf.ID).Int("mention_persons", mentions).
									Msg("Skipping fold: matched fact is shared by several persons; keeping standalone")
								continue
							}
							sfCopy := sf
							foldTarget = &sfCopy
							break
						}
						return 0, 0, false, nil
					}
				}
			}
		}
	}

	// Insert the fact
	factID, err := fi.storer.InsertFact(ctx, input)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to insert fact: %w", err)
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
		return 0, 0, false, fmt.Errorf("failed to link source: %w", err)
	}

	// Link the subject plus any co-actors to this source mention. Co-actors
	// share full ownership of the fact (see StoreFactInput.CoActorIDs); the
	// mention insert is ON CONFLICT DO NOTHING so a duplicate id is harmless.
	mentionIDs := make([]int64, 0, 1+len(input.CoActorIDs))
	mentionIDs = append(mentionIDs, subjectID)
	for _, id := range input.CoActorIDs {
		if id > 0 && id != subjectID {
			mentionIDs = append(mentionIDs, id)
		}
	}
	if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, mentionIDs); err != nil {
		fi.compensateDelete(ctx, factID, "linking subject entity")
		return 0, 0, false, fmt.Errorf("failed to link subject entity: %w", err)
	}

	// Fold path: the fact is a same-claim restatement — attach it to a meta-fact
	// instead of running stance analysis (the meta carries the claim; analyzing
	// the member would only duplicate LLM calls and contradictions). The member
	// inherits the meta's stance so the every-fact-has-a-stance invariant holds.
	// On any fold error we fall through to normal stance processing (fail-open).
	if foldTarget != nil {
		if foldErr := fi.foldNewFact(ctx, subjectID, factID, input, *foldTarget); foldErr != nil {
			log.Warn().Err(foldErr).Int64("fact_id", factID).Int64("matched_fact_id", foldTarget.ID).
				Msg("Meta-fold failed; falling back to normal stance routing")
		} else {
			return factID, subjectID, true, nil
		}
	}

	// Mandatory stance processing. Failure rolls back the fact via compensating
	// delete so that the "every fact has a stance" invariant holds.
	stanceResult, err := fi.stanceProcessor.ProcessNewFact(ctx, subjectID, factID, input.NormalizedFactText, input.Embedding, input.CanonicalVerb)
	if err != nil {
		fi.compensateDelete(ctx, factID, "stance processing")
		return 0, 0, false, fmt.Errorf("stance processing failed for fact %d: %w", factID, err)
	}

	log.Info().
		Int64("fact_id", factID).
		Int64("source_id", sourceID).
		Str("subject_entity", input.SubjectEntity).
		Str("verdict", stanceResult.Verdict).
		Int64("stance_id", stanceResult.StanceID).
		Msg("Inserted fact and routed through stance")

	return factID, subjectID, true, nil
}

func (fi *FactInserter) foldingEnabled() bool {
	return fi.metaStorer != nil && fi.metaSynth != nil
}

// foldNewFact links a freshly inserted same-claim fact under a meta-fact:
// resolves (or creates) the meta of the matched fact, re-synthesizes the meta
// text over all member texts, marks the new fact as a member ('duplicate'
// relation), repoints any contradictions, and attaches the member to the meta's
// stance for display. Raw member facts are never deleted.
func (fi *FactInserter) foldNewFact(ctx context.Context, personID, newFactID int64, input StoreFactInput, matched SimilarFact) error {
	ms, synth := fi.metaStorer, fi.metaSynth

	metaID, found, err := ms.GetMetaForFact(ctx, matched.ID)
	if err != nil {
		return fmt.Errorf("resolve meta of fact %d: %w", matched.ID, err)
	}

	if !found {
		text, err := synth.SynthesizeMetaFact(ctx, input.SubjectEntity,
			[]string{matched.Text, input.NormalizedFactText}, input.Lang)
		if err != nil {
			return fmt.Errorf("synthesize new meta: %w", err)
		}
		metaID, err = ms.CreateMetaFact(ctx, personID, text, input.EmbeddingText, input.Embedding, input.Lang, input.CanonicalVerb)
		if err != nil {
			return fmt.Errorf("create meta: %w", err)
		}
		// The meta takes over the matched fact's stance slot (this person's).
		if stanceID, ok, sErr := ms.GetFactStanceID(ctx, personID, matched.ID); sErr == nil && ok {
			if aErr := ms.AddFactToStance(ctx, stanceID, metaID); aErr != nil {
				log.Warn().Err(aErr).Int64("meta_fact_id", metaID).Msg("Failed to attach meta to stance")
			}
		}
		if err := ms.MarkFactAsDuplicate(ctx, matched.ID, metaID); err != nil {
			return fmt.Errorf("mark matched fact %d as member: %w", matched.ID, err)
		}
		if _, _, err := ms.RepointFactDuplicate(ctx, metaID, matched.ID); err != nil {
			return fmt.Errorf("repoint matched fact %d onto meta: %w", matched.ID, err)
		}
	} else {
		members, err := ms.GetMetaMemberTexts(ctx, metaID)
		if err != nil {
			return fmt.Errorf("load meta %d members: %w", metaID, err)
		}
		members = append(members, input.NormalizedFactText)
		text, err := synth.SynthesizeMetaFact(ctx, input.SubjectEntity, members, input.Lang)
		if err != nil {
			return fmt.Errorf("re-synthesize meta %d: %w", metaID, err)
		}
		if err := ms.UpdateMetaFactText(ctx, metaID, text); err != nil {
			return fmt.Errorf("update meta %d text: %w", metaID, err)
		}
	}

	if err := ms.MarkFactAsDuplicate(ctx, newFactID, metaID); err != nil {
		return fmt.Errorf("mark new fact %d as member of meta %d: %w", newFactID, metaID, err)
	}
	if _, _, err := ms.RepointFactDuplicate(ctx, metaID, newFactID); err != nil {
		return fmt.Errorf("repoint new fact %d onto meta: %w", newFactID, err)
	}
	// Member joins the meta's stance so the every-fact-has-a-stance invariant
	// holds without any stance-analysis LLM call.
	if stanceID, ok, sErr := ms.GetFactStanceID(ctx, personID, metaID); sErr == nil && ok {
		if aErr := ms.AddFactToStance(ctx, stanceID, newFactID); aErr != nil {
			log.Warn().Err(aErr).Int64("fact_id", newFactID).Msg("Failed to attach member to meta's stance")
		}
	}
	if err := ms.RefreshPersonFactCount(ctx, personID); err != nil {
		log.Warn().Err(err).Int64("person_id", personID).Msg("Failed to refresh fact count after fold")
	}

	log.Info().
		Int64("fact_id", newFactID).
		Int64("meta_fact_id", metaID).
		Int64("matched_fact_id", matched.ID).
		Bool("meta_created", !found).
		Str("fact", truncate(input.NormalizedFactText, 80)).
		Msg("Folded same-claim fact under meta-fact")
	return nil
}

// compensateDelete rolls back a partially inserted fact. Errors here are logged
// but cannot be propagated (we are already in the error path). A separate audit
// metric flags any orphan facts that slip through.
func (fi *FactInserter) compensateDelete(ctx context.Context, factID int64, stage string) {
	if delErr := fi.storer.DeleteFact(ctx, factID); delErr != nil {
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

// truncate truncates a string to maxLen runes, appending "..." if truncated.
// Operates on runes (not bytes) so it never splits a multi-byte UTF-8 codepoint
// — Postgres rejects invalid UTF-8 with SQLSTATE 22021.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

// isDirectClaimVerb returns true if the verb represents a direct claim or action
// by the person (as opposed to an external attribution like a court ruling).
// Duplicated from categorizer.IsDirectClaimVerb to avoid import cycle.
func isDirectClaimVerb(verb string) bool {
	switch verb {
	case "DENIED", "CLAIMED", "PROMISED", "SUPPORTED",
		"OPPOSED", "VOTED_FOR", "VOTED_AGAINST", "PROPOSED",
		"CONFIRMED", "ADMITTED", "REFUSED", "DEMANDED",
		"THREATENED", "ANNOUNCED", "CHANGED_POSITION":
		return true
	default:
		return false
	}
}
