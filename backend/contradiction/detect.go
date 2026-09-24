package contradiction

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// DetectOptions configures RunDetection.
type DetectOptions struct {
	// Persons restricts the run to the given persons; empty = every person, in
	// GetAllPersons order. Resolving names to persons is the caller's job.
	Persons []storage.PersonInfo
	// Exclude lists normalized person names to skip.
	Exclude []string
	// MaxPersons bounds the run to the first N persons of the set above, in
	// its order (0 = all).
	MaxPersons int
	// DryRun judges pairs but records nothing: no contradictions, pair checks,
	// duplicate marks, queued-pair consumption or job metrics.
	DryRun bool
	// CheckAll pairs every fact with every other instead of its KNN neighbors.
	CheckAll bool
	// OnVerdict receives every pair the detector judged, after the
	// verification pass and before it is recorded. Nil = silent.
	OnVerdict func(PairVerdict)
}

// DetectResult is what a detection run judged and found.
type DetectResult struct {
	PairsChecked   int
	Contradictions int
}

// PairVerdict is one judged pair as reported to DetectOptions.OnVerdict.
type PairVerdict struct {
	Person    string
	FactAID   int64
	FactAText string
	FactBID   int64
	FactBText string
	// Flagged is the detector's verdict; Contradiction is the final one, false
	// when the verification pass refuted a flagged pair.
	Flagged       bool
	Contradiction bool
	Reason        string
	// Reasoning is the detector's analysis, or its raw response when it gave none.
	Reasoning string
}

// pairInfo holds information about a fact pair to check
type pairInfo struct {
	factAID   int64
	factAText string
	factADate *time.Time
	factBID   int64
	factBText string
	factBDate *time.Time
	// alsoA / alsoB are the other wordings of a statement that stands for a
	// group of restatements; the judge and its verifier both see them.
	alsoA, alsoB []string
	// similarity is the cosine similarity (1 - distance) between the two facts
	// from the KNN neighbor search, used to gate the dedup-merge check on the
	// "not a contradiction" path. 0 when unknown (e.g. the --check-all path).
	similarity float64
	// queued marks a pair a producer asked for (fact_pair_queue): it bypasses
	// the external-verb filter — the producer vouches for the pairing.
	queued bool
}

// queuedPairsPerPersonPerNight bounds how many queued pairs one person's
// detection consumes per run; the rest stay queued for the next night.
const queuedPairsPerPersonPerNight = 300

// contradictionDedupSimFloor is the minimum KNN cosine similarity for the
// contradiction-pass dedup net to even ask the LLM "same claim?" on a
// non-contradictory pair. Matches the paraphrase-skip threshold used in
// paraphraseSkip, so we only spend an LLM call on pairs that are genuinely
// near-duplicate.
const contradictionDedupSimFloor = 0.80

// personPairs holds collected pairs for a single person, ready for judging.
type personPairs struct {
	personID   int64
	personName string
	pairs      []pairInfo
}

// RunDetection collects the candidate pairs of every person in scope (KNN
// walk or every pair with CheckAll, plus the producer queue) and judges them
// with the LLM detector, the inline verification pass gating each YES before
// it is recorded. Returns zero counts without touching the LLM when there is
// nothing new to check.
func RunDetection(ctx context.Context, storer *storage.PostgresStorer, opts DetectOptions) (DetectResult, error) {
	persons, err := personsForDetection(ctx, storer, opts)
	if err != nil {
		return DetectResult{}, err
	}

	excludeSet := make(map[string]struct{}, len(opts.Exclude))
	for _, name := range opts.Exclude {
		excludeSet[name] = struct{}{}
	}

	var allPersonPairs []personPairs
	totalPairs := 0
	for _, person := range persons {
		if _, excluded := excludeSet[person.Name]; excluded {
			log.Info().Str("person", person.Name).Msg("Skipping excluded person")
			continue
		}
		pairs := collectPairsForPerson(ctx, storer, person, opts)
		if len(pairs) > 0 {
			allPersonPairs = append(allPersonPairs, personPairs{
				personID:   person.ID,
				personName: person.Name,
				pairs:      pairs,
			})
			totalPairs += len(pairs)
		}
	}

	if totalPairs == 0 {
		return DetectResult{}, nil
	}

	return judgePairs(ctx, storer, allPersonPairs, opts)
}

func personsForDetection(ctx context.Context, storer *storage.PostgresStorer, opts DetectOptions) ([]storage.PersonInfo, error) {
	persons := opts.Persons
	if len(persons) == 0 {
		var err error
		persons, err = storer.GetAllPersons(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get persons: %w", err)
		}
		log.Info().Int("count", len(persons)).Msg("Processing all persons")
	}
	return firstN(persons, opts.MaxPersons), nil
}

// firstN keeps the first n items (n <= 0 = all), logging when it truncates.
func firstN[T any](items []T, n int) []T {
	if n <= 0 || len(items) <= n {
		return items
	}
	log.Info().Int("max_persons", n).Int("persons", len(items)).Msg("Bounding run to the first persons")
	return items[:n]
}

// pairKey is the order-independent "%d-%d" key of a fact pair, smaller ID
// first, as fact_pair_checks stores it.
func pairKey(a, b int64) string {
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("%d-%d", a, b)
}

// collectPairsForPerson finds all candidate contradiction pairs for a person,
// applying dedup, verb filtering, and checked-pair filtering.
func collectPairsForPerson(ctx context.Context, storer *storage.PostgresStorer, person storage.PersonInfo, opts DetectOptions) []pairInfo {
	facts, err := storer.GetFactsWithEmbeddingsForPerson(ctx, person.ID)
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Failed to get facts, skipping")
		return nil
	}

	if len(facts) < 2 {
		log.Debug().Str("person", person.Name).Int("facts", len(facts)).Msg("Not enough facts to compare")
		return nil
	}

	log.Info().Str("person", person.Name).Int("facts", len(facts)).Msg("Checking facts for contradictions")

	allPairs, sameVerbSkipped, err := candidatePairs(ctx, storer, person, facts, opts.CheckAll)
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Failed to find neighbors, skipping")
		return nil
	}

	// Queued pairs: producers (e.g. frpolitics pairing a vote with what its
	// author said in the same séance) asked for these regardless of embedding
	// distance. Consumed (taken and deleted) here, budgeted per person, then
	// they join the normal flow below (dedup, checked-pair filter, judge). A
	// queued fact that is no longer active simply drops; anything not judged
	// tonight is re-enqueued by the producer's next idempotent run. Appended
	// AFTER the KNN candidates so a pair KNN also found keeps its similarity.
	if !opts.DryRun {
		if queued, qErr := storer.ConsumeQueuedPairsForPerson(ctx, person.ID, queuedPairsPerPersonPerNight); qErr != nil {
			log.Error().Err(qErr).Str("person", person.Name).Msg("Failed to consume queued pairs; continuing with KNN only")
		} else if len(queued) > 0 {
			byID := make(map[int64]storage.FactWithEmbedding, len(facts))
			for _, f := range facts {
				byID[f.ID] = f
			}
			added := 0
			for _, q := range queued {
				a, okA := byID[q.FactAID]
				b, okB := byID[q.FactBID]
				if !okA || !okB {
					continue
				}
				allPairs = append(allPairs, pairInfo{
					factAID: a.ID, factAText: a.Text, factADate: a.PublishedAt,
					factBID: b.ID, factBText: b.Text, factBDate: b.PublishedAt,
					queued: true,
				})
				added++
			}
			log.Info().Str("person", person.Name).Int("queued", len(queued)).Int("usable", added).Msg("Consumed queued pairs into candidates")
		}
	}

	allPairs = dedupePairs(person.Name, allPairs)

	verbByFact := make(map[int64]string, len(facts))
	for _, f := range facts {
		verbByFact[f.ID] = f.CanonicalVerb
	}
	allPairs = dropExternalOnlyPairs(person.Name, allPairs, verbByFact)

	checkedPairs, err := storer.GetCheckedPairs(ctx, person.ID)
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Failed to get checked pairs")
		return nil
	}
	filteredPairs := dropCheckedPairs(person.Name, allPairs, checkedPairs)
	if err := attachGroupMembers(ctx, storer, filteredPairs); err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Failed to load group members; skipping person")
		return nil
	}

	if sameVerbSkipped > 0 {
		log.Info().Str("person", person.Name).Int("skipped", sameVerbSkipped).Msg("Skipped same-verb pairs (paraphrases)")
	}

	log.Info().Str("person", person.Name).Int("pairs_to_check", len(filteredPairs)).Msg("Collected pairs for processing")
	return filteredPairs
}

// groupSameClaim puts two active facts found to be the same claim into one
// claim group headed by the more concrete of the two.
func groupSameClaim(ctx context.Context, storer *storage.PostgresStorer, a, b int64) error {
	parent, err := storer.MostConcreteFact(ctx, []int64{a, b})
	if err != nil {
		return err
	}
	from := a
	if parent == a {
		from = b
	}
	return storer.MergeFactIntoGroup(ctx, from, parent)
}

// attachGroupMembers gives every pair side that stands for a group of
// restatements its other wordings, in one lookup for all the pairs.
func attachGroupMembers(ctx context.Context, storer *storage.PostgresStorer, pairs []pairInfo) error {
	if len(pairs) == 0 {
		return nil
	}
	ids := make([]int64, 0, 2*len(pairs))
	for _, p := range pairs {
		ids = append(ids, p.factAID, p.factBID)
	}
	members, err := storer.GroupMemberTexts(ctx, ids)
	if err != nil {
		return err
	}
	for i := range pairs {
		pairs[i].alsoA = members[pairs[i].factAID]
		pairs[i].alsoB = members[pairs[i].factBID]
	}
	return nil
}

// candidatePairs pairs each fact with its KNN neighbors (or every other fact
// with checkAll), each partner already replaced by its most general ancestor
// (one statement per person, see storage.NeighborPair), and skips
// near-duplicate paraphrases among the KNN pairs. Returns the pairs and the
// paraphrase-skip count.
func candidatePairs(ctx context.Context, storer *storage.PostgresStorer, person storage.PersonInfo, facts []storage.FactWithEmbedding, checkAll bool) ([]pairInfo, int, error) {
	neighborPairs, err := storer.GetNeighborPairsForPerson(ctx, person.ID, checkAll)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]storage.FactWithEmbedding, len(facts))
	for _, f := range facts {
		byID[f.ID] = f
	}

	var allPairs []pairInfo
	sameVerbSkipped := 0
	for _, np := range neighborPairs {
		fact, ok := byID[np.FactID]
		if !ok {
			// Became active after the person's facts were loaded; it is not one
			// of the facts this run walks.
			continue
		}
		// The KNN similarity gates the paraphrase skip here and the dedup net
		// in judgePairs; check-all pairs carry none (0).
		similarity := 0.0
		if !checkAll {
			similarity = 1.0 - np.Distance
			if skip, bothExternal := paraphraseSkip(fact.CanonicalVerb, np.NeighborVerb, similarity); skip {
				sameVerbSkipped++
				log.Debug().
					Int64("fact_a", fact.ID).
					Int64("fact_b", np.NeighborID).
					Str("verb_a", fact.CanonicalVerb).
					Str("verb_b", np.NeighborVerb).
					Bool("both_external", bothExternal).
					Float64("similarity", similarity).
					Msg("Skipping paraphrase pair")
				continue
			}
		}
		allPairs = append(allPairs, pairInfo{
			factAID:    fact.ID,
			factAText:  fact.Text,
			factADate:  fact.PublishedAt,
			factBID:    np.TargetID,
			factBText:  np.TargetText,
			factBDate:  np.TargetDate,
			similarity: similarity,
		})
	}

	return allPairs, sameVerbSkipped, nil
}

// paraphraseSkip reports whether a KNN pair is a paraphrase to skip rather
// than judge: both facts carry a canonical verb, they sit above 0.80 cosine
// similarity, and either the verbs are the same or both are external
// attributions. bothExternal is returned for logging.
func paraphraseSkip(verbA, verbB string, similarity float64) (skip, bothExternal bool) {
	if verbA == "" || verbB == "" || similarity <= 0.80 {
		return false, false
	}
	bothExternal = !categorizer.IsDirectClaimVerb(verbA) && !categorizer.IsDirectClaimVerb(verbB)
	return verbA == verbB || bothExternal, bothExternal
}

// dedupePairs drops self-pairs and repeated (order-independent) pairs, keeping
// the first occurrence; a queued repeat marks the kept pair queued.
func dedupePairs(person string, allPairs []pairInfo) []pairInfo {
	seen := make(map[string]int, len(allPairs))
	dedupedPairs := make([]pairInfo, 0, len(allPairs))
	selfPairs := 0
	for _, pair := range allPairs {
		if pair.factAID == pair.factBID {
			selfPairs++
			continue
		}
		key := pairKey(pair.factAID, pair.factBID)
		if idx, exists := seen[key]; !exists {
			seen[key] = len(dedupedPairs)
			dedupedPairs = append(dedupedPairs, pair)
		} else if pair.queued {
			dedupedPairs[idx].queued = true
		}
	}
	removed := len(allPairs) - len(dedupedPairs)
	if removed > 0 {
		log.Info().Str("person", person).
			Int("duplicate_pairs_removed", removed-selfPairs).
			Int("self_pairs_removed", selfPairs).
			Msg("Cleaned up pairs from ancestor walking")
	}
	return dedupedPairs
}

// dropExternalOnlyPairs filters out pairs where neither fact has a direct
// claim verb; a queued pair is kept regardless (the producer vouches for it).
func dropExternalOnlyPairs(person string, allPairs []pairInfo, verbByFact map[int64]string) []pairInfo {
	var directPairs []pairInfo
	externalPairsSkipped := 0
	for _, pair := range allPairs {
		verbA := verbByFact[pair.factAID]
		verbB := verbByFact[pair.factBID]
		if !pair.queued && !categorizer.IsDirectClaimVerb(verbA) && !categorizer.IsDirectClaimVerb(verbB) {
			externalPairsSkipped++
			continue
		}
		directPairs = append(directPairs, pair)
	}
	if externalPairsSkipped > 0 {
		log.Info().Str("person", person).Int("skipped", externalPairsSkipped).Msg("Skipped external-only pairs (no direct claim verb)")
	}
	return directPairs
}

// dropCheckedPairs filters out pairs that have already been judged
// (fact_pair_checks, keyed as GetCheckedPairs returns them).
func dropCheckedPairs(person string, allPairs []pairInfo, checkedPairs map[string]bool) []pairInfo {
	var filteredPairs []pairInfo
	for _, pair := range allPairs {
		if !checkedPairs[pairKey(pair.factAID, pair.factBID)] {
			filteredPairs = append(filteredPairs, pair)
		}
	}
	skipped := len(allPairs) - len(filteredPairs)
	if skipped > 0 {
		log.Info().Str("person", person).Int("skipped", skipped).Msg("Skipped pairs with existing checks")
	}
	return filteredPairs
}

// judgePairs runs the detector over every collected pair, verifies each YES,
// records verdicts, contradictions and duplicates, and writes the job metrics.
func judgePairs(ctx context.Context, storer *storage.PostgresStorer, allPersonPairs []personPairs, opts DetectOptions) (DetectResult, error) {
	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return DetectResult{}, fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	// Bundler is best-effort: if either embedder or LLM is unavailable we still record
	// raw contradictions, just without the LLM-driven bundle resolution. Cross-stance
	// rows then stay as solo bundles until the next run.
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Embedding generator unavailable; new contradictions will not be bundled by LLM fallback")
	}
	var bundler *Bundler
	if embedder != nil {
		bundler, err = NewBundlerFromEnv(storer, embedder)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to construct contradiction Bundler; new contradictions will not be bundled by LLM fallback")
		}
	}

	// Best-effort dedup net for the "not a contradiction" path: a highly similar
	// non-contradictory pair may be the same claim restated (the embedding is
	// stance-blind by design, so the judge is the authority). If the adjudicator
	// confirms they are the same claim, we mark the pair a duplicate so it stops
	// generating future pairs/contradictions. Nil = disabled (no LLM env).
	var dedupeJudge sameClaimJudge
	if adj, aErr := core.NewFactDedupeAdjudicatorFromEnv(); aErr != nil {
		log.Warn().Err(aErr).Msg("Fact-dedupe adjudicator unavailable; contradiction-pass dedup disabled")
	} else {
		dedupeJudge = adj
	}

	totalContradictions := 0
	totalPairsChecked := 0
	// unavailable is set when the LLM went away: the run stops at that pair
	// and every pair not yet recorded stays pending for the next run.
	var unavailable error

	for _, pp := range allPersonPairs {
		if unavailable != nil {
			break
		}
		personContradictions := 0
		batchSize := MaxPairsPerBatchSync

		for i := 0; i < len(pp.pairs); i += batchSize {
			end := i + batchSize
			if end > len(pp.pairs) {
				end = len(pp.pairs)
			}
			batch := pp.pairs[i:end]

			factPairs := make([]FactPair, len(batch))
			for j, p := range batch {
				factPairs[j] = FactPair{
					IndexA: j,
					TextA:  p.factAText,
					DateA:  p.factADate,
					IndexB: j,
					TextB:  p.factBText,
					DateB:  p.factBDate,
					AlsoA:  p.alsoA,
					AlsoB:  p.alsoB,
				}
			}

			results := detector.AreContradictoryBatch(ctx, factPairs)
			totalPairsChecked += len(batch)

			var pairResults []storage.PairCheckResult
			for j, result := range results {
				d, dErr := decidePair(ctx, detector, dedupeJudge, pp.personName, batch[j], result, opts.DryRun)
				if dErr != nil {
					unavailable = dErr
					break
				}
				if !d.decided {
					continue
				}
				isContra := d.contradiction

				if opts.OnVerdict != nil {
					reasoning := result.Reasoning
					if reasoning == "" {
						reasoning = result.RawResponse
					}
					opts.OnVerdict(PairVerdict{
						Person:        pp.personName,
						FactAID:       batch[j].factAID,
						FactAText:     batch[j].factAText,
						FactBID:       batch[j].factBID,
						FactBText:     batch[j].factBText,
						Flagged:       result.IsContradictory,
						Contradiction: isContra,
						Reason:        result.Reason,
						Reasoning:     reasoning,
					})
				}

				pairResults = append(pairResults, storage.PairCheckResult{
					FactAID:         batch[j].factAID,
					FactBID:         batch[j].factBID,
					IsContradiction: isContra,
				})

				if d.sameClaim {
					if gErr := groupSameClaim(ctx, storer, batch[j].factAID, batch[j].factBID); gErr != nil {
						log.Error().Err(gErr).
							Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
							Msg("Failed to group same-claim facts")
					} else {
						log.Info().
							Str("person", pp.personName).
							Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
							Float64("similarity", batch[j].similarity).
							Msg("Grouped same-claim facts (contradiction-pass dedup)")
					}
				}

				if isContra {
					totalContradictions++
					personContradictions++

					log.Warn().
						Str("person", pp.personName).
						Int64("fact_a", batch[j].factAID).
						Int64("fact_b", batch[j].factBID).
						Str("reason", result.Reason).
						Msg("Contradiction detected")

					if !opts.DryRun {
						contradictionID, needsBundler, err := storer.RecordContradictionImmediate(ctx, pp.personID, batch[j].factAID, batch[j].factBID, result.Reason, result.ReasonEn)
						if err != nil {
							log.Error().Err(err).Msg("Failed to record contradiction")
						} else {
							log.Info().Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Contradiction recorded and committed")
							if needsBundler && bundler != nil {
								if bErr := resolveBundle(ctx, bundler, pp.personID, contradictionID,
									batch[j].factAID, batch[j].factBID,
									batch[j].factAText, batch[j].factBText, result.Reason); bErr != nil {
									unavailable = bErr
									break
								}
							}
						}
					}
				}
			}

			if !opts.DryRun && len(pairResults) > 0 {
				if err := storer.RecordPairChecksBatch(ctx, pp.personID, JudgeRulebookHash(), pairResults); err != nil {
					log.Error().Err(err).Str("person", pp.personName).Msg("Failed to record pair checks")
				}
			}

			if unavailable != nil {
				break
			}

			log.Debug().Int("batch_start", i).Int("batch_end", end).Int("total_pairs", len(pp.pairs)).Str("person", pp.personName).Msg("Batch processed")

			if end < len(pp.pairs) {
				time.Sleep(DelayBetweenBatches)
			}
		}

		if personContradictions > 0 {
			log.Info().Str("person", pp.personName).Int("contradictions", personContradictions).Msg("Contradictions found and saved")
		}
	}

	if !opts.DryRun {
		jobMetrics := []storage.JobMetric{
			{JobType: "contradictions-detect", Target: "", Metric: "pairs_checked", Value: float64(totalPairsChecked)},
			{JobType: "contradictions-detect", Target: "", Metric: "contradictions_found", Value: float64(totalContradictions)},
		}
		if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
			log.Error().Err(err).Msg("Failed to record job metrics")
		}
	}

	res := DetectResult{PairsChecked: totalPairsChecked, Contradictions: totalContradictions}
	if unavailable != nil {
		return res, fmt.Errorf("contradiction detection stopped: %w", unavailable)
	}
	return res, nil
}

// pairVerifier is the refutation pass run on each YES before it is recorded.
type pairVerifier interface {
	VerifyContradiction(ctx context.Context, a, b promptrules.Statement, claimedReason string) (keep bool, reason string, err error)
}

// sameClaimJudge is the dedup net run on near-identical NOs.
type sameClaimJudge interface {
	IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error)
}

// pairDecision is what judgePairs records for one judged pair.
type pairDecision struct {
	// decided is false when nothing may be recorded: the pair stays pending
	// and is judged again by the next run.
	decided       bool
	contradiction bool
	// sameClaim asks for the two facts to be grouped as one claim.
	sameClaim bool
}

// bundleResolver is the part of *Bundler the detection uses.
type bundleResolver interface {
	Resolve(ctx context.Context, personID, contradictionID, factAID, factBID int64, factAText, factBText, reason string) error
}

// resolveBundle runs the bundler on a newly recorded contradiction. A failure
// leaves the row a solo bundle and is only logged, except an unavailable LLM
// (llm.ErrUnavailable), which is returned so the run stops: the row stays solo
// until the person's next rebundle (RebundlePerson, run by the stance
// fan-out for persons with new facts).
func resolveBundle(ctx context.Context, bundler bundleResolver, personID, contradictionID, factAID, factBID int64, factAText, factBText, reason string) error {
	err := bundler.Resolve(ctx, personID, contradictionID, factAID, factBID, factAText, factBText, reason)
	if err == nil {
		return nil
	}
	log.Warn().Err(err).Int64("contradiction_id", contradictionID).
		Msg("Bundler resolution failed; row remains a solo bundle")
	if errors.Is(err, llm.ErrUnavailable) {
		return fmt.Errorf("bundling contradiction %d: %w", contradictionID, err)
	}
	return nil
}

// decidePair turns the detector's verdict on one pair into the decision to
// record. A YES goes through the verifier; a NO between near-identical facts
// through the dedup net (skipped in dry runs).
//
// No decision is made when the detector failed, when the LLM was unavailable
// to the verifier or the dedup net, or when the dedup net failed otherwise: a
// recorded NO is never judged again, so it would lose the same-claim grouping
// for good. A verifier failure other than llm.ErrUnavailable keeps the YES
// (fail-open, see OpenAIContradictionDetector.VerifyContradiction): the judge
// found it, and a verifier that cannot answer must not silently drop it; the
// auditor re-judges it later.
//
// The error is non-nil only when the LLM was unavailable (it wraps
// llm.ErrUnavailable): the caller stops the run there.
func decidePair(ctx context.Context, verifier pairVerifier, dedupe sameClaimJudge, person string, p pairInfo, r BatchResult, dryRun bool) (pairDecision, error) {
	if r.Error != nil {
		if errors.Is(r.Error, llm.ErrUnavailable) {
			return pairDecision{}, r.Error
		}
		log.Error().Err(r.Error).Int64("fact_a", p.factAID).Int64("fact_b", p.factBID).Msg("Detector error")
		return pairDecision{}, nil
	}

	d := pairDecision{decided: true, contradiction: r.IsContradictory}
	// A pair whose fact text is empty (deleted mid-run) cannot be verified;
	// the YES is kept, mirroring the stance path.
	if d.contradiction && p.factAText != "" && p.factBText != "" {
		keep, refutation, err := verifier.VerifyContradiction(ctx,
			promptrules.Statement{Text: p.factAText, Also: p.alsoA},
			promptrules.Statement{Text: p.factBText, Also: p.alsoB}, r.Reason)
		switch {
		case errors.Is(err, llm.ErrUnavailable):
			return pairDecision{}, err
		case err != nil:
			log.Warn().Err(err).Int64("fact_a", p.factAID).Int64("fact_b", p.factBID).
				Msg("Contradiction verification failed; keeping contradiction (fail-open)")
		case !keep:
			d.contradiction = false
			metrics.RecordDecision(metrics.StageContradiction, metrics.OutcomeContradictionRefuted)
			log.Info().Str("person", person).
				Int64("fact_a", p.factAID).Int64("fact_b", p.factBID).
				Str("flagged_reason", r.Reason).Str("refutation", refutation).
				Msg("Contradiction refuted by verification pass; dropping")
		}
	}

	if !d.contradiction && dedupe != nil && !dryRun &&
		p.similarity >= contradictionDedupSimFloor && p.factAText != "" && p.factBText != "" {
		same, err := dedupe.IsSameClaim(ctx, person, p.factAText, p.factBText)
		switch {
		case errors.Is(err, llm.ErrUnavailable):
			return pairDecision{}, err
		case err != nil:
			log.Warn().Err(err).Int64("fact_a", p.factAID).Int64("fact_b", p.factBID).
				Msg("Contradiction-pass dedup adjudication failed; pair left pending")
			return pairDecision{}, nil
		}
		d.sameClaim = same
	}
	return d, nil
}
