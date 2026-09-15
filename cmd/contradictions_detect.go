package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	detectPersonFlag  string
	detectExcludeFlag []string
	detectDryRun      bool
	detectCheckAll    bool
	detectDebug       bool
)

// pairInfo holds information about a fact pair to check
type pairInfo struct {
	factAID   int64
	factAText string
	factADate *time.Time
	factBID   int64
	factBText string
	factBDate *time.Time
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
// non-contradictory pair. Matches the paraphrase-skip threshold used above, so
// we only spend an LLM call on pairs that are genuinely near-duplicate.
const contradictionDedupSimFloor = 0.80

// neighborCandidate is a fact paired against the current fact during detection,
// carrying the KNN cosine similarity so the dedup-merge check can be gated on it.
type neighborCandidate struct {
	ID          int64
	Text        string
	PublishedAt *time.Time
	Similarity  float64
}

var contradictionsDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Run contradiction detection on existing facts.",
	Long: `Analyzes stored facts to find contradictions using semantic similarity
and LLM-based comparison. By default, processes all persons. Use --person flag
to process only a specific person. Use --dry-run to see what would be detected
without recording to the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runContradictionDetection,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsDetectCmd)
	contradictionsDetectCmd.Flags().StringVarP(&detectPersonFlag, "person", "p", "", "Specific person name to process (optional)")
	contradictionsDetectCmd.Flags().StringSliceVar(&detectExcludeFlag, "exclude", nil, "Person names to skip (comma-separated)")
	contradictionsDetectCmd.Flags().BoolVar(&detectDryRun, "dry-run", false, "Show detected contradictions without recording to database")
	contradictionsDetectCmd.Flags().BoolVar(&detectCheckAll, "check-all", false, "Check all fact pairs, not just semantically similar ones")
	contradictionsDetectCmd.Flags().BoolVar(&detectDebug, "debug", false, "Print LLM reasoning and verdict for every pair")
}

// runContradictionDetection is the CLI wrapper that delegates to executeContradictionDetection.
func runContradictionDetection(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	opts := ContradictionDetectOpts{
		Person:   detectPersonFlag,
		Exclude:  detectExcludeFlag,
		DryRun:   detectDryRun,
		CheckAll: detectCheckAll,
		Debug:    detectDebug,
	}

	return executeContradictionDetection(ctx, storer, opts)
}

// ContradictionDetectOpts configures contradiction detection, used by both CLI and pipeline scheduler.
type ContradictionDetectOpts struct {
	Person   string   // specific person name (empty = all)
	Exclude  []string // person names to skip
	DryRun   bool
	CheckAll bool // check all pairs, not just semantically similar
	Debug    bool // print LLM reasoning for every pair
}

// personPairs holds collected pairs for a single person, ready for either sync or batch processing.
type personPairs struct {
	personID   int64
	personName string
	pairs      []pairInfo
}

// executeContradictionDetection runs contradiction detection with the given options.
// Called by both the CLI command and the daily pipeline. Processes pairs
// synchronously via the OPEN_AI_API LLM, with the inline verification pass
// gating each YES before it is recorded.
func executeContradictionDetection(ctx context.Context, storer *storage.PostgresStorer, opts ContradictionDetectOpts) error {
	// Get persons to process
	persons, err := getPersonsForDetection(ctx, storer, opts)
	if err != nil {
		return err
	}

	// Build exclude set
	excludeSet := make(map[string]struct{}, len(opts.Exclude))
	for _, name := range opts.Exclude {
		excludeSet[util.NormalizeString(name)] = struct{}{}
	}

	// Collect pairs for all persons
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
		fmt.Printf("\n=== SUMMARY ===\nNo new pairs to check.\n")
		return nil
	}

	return processSyncContradictions(ctx, storer, allPersonPairs, opts)
}

func getPersonsForDetection(ctx context.Context, storer *storage.PostgresStorer, opts ContradictionDetectOpts) ([]storage.PersonInfo, error) {
	if opts.Person != "" {
		normalizedName := util.NormalizeString(opts.Person)
		personID, err := storer.GetPersonIDByName(ctx, normalizedName)
		if err != nil {
			return nil, fmt.Errorf("failed to find person %q: %w", opts.Person, err)
		}
		log.Info().Str("person", normalizedName).Int64("id", personID).Msg("Processing single person")
		return []storage.PersonInfo{{ID: personID, Name: normalizedName}}, nil
	}
	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get persons: %w", err)
	}
	log.Info().Int("count", len(persons)).Msg("Processing all persons")
	return persons, nil
}

// collectPairsForPerson finds all candidate contradiction pairs for a person,
// applying dedup, verb filtering, and checked-pair filtering.
func collectPairsForPerson(ctx context.Context, storer *storage.PostgresStorer, person storage.PersonInfo, opts ContradictionDetectOpts) []pairInfo {
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

	var allPairs []pairInfo

	var readTx pgx.Tx
	if !opts.CheckAll {
		var txErr error
		readTx, txErr = storer.Pool().Begin(ctx)
		if txErr != nil {
			log.Error().Err(txErr).Str("person", person.Name).Msg("Failed to begin read transaction, skipping")
			return nil
		}
	}

	sameVerbSkipped := 0

	for _, fact := range facts {
		var pairsToCheck []neighborCandidate

		if opts.CheckAll {
			for _, other := range facts {
				if other.ID > fact.ID {
					pairsToCheck = append(pairsToCheck, neighborCandidate{
						ID: other.ID, Text: other.Text, PublishedAt: other.PublishedAt, Similarity: 0})
				}
			}
		} else {
			neighbors, err := storer.FindNeighborFactsForPerson(ctx, readTx, person.ID, fact.Embedding, fact.ID)
			if err != nil {
				log.Error().Err(err).Int64("fact_id", fact.ID).Msg("Failed to find neighbors")
				continue
			}
			for _, neighbor := range neighbors {
				if neighbor.ID > fact.ID {
					if fact.CanonicalVerb != "" && neighbor.CanonicalVerb != "" &&
						(1.0-neighbor.Distance) > 0.80 {
						sameVerb := fact.CanonicalVerb == neighbor.CanonicalVerb
						bothExternal := !categorizer.IsDirectClaimVerb(fact.CanonicalVerb) && !categorizer.IsDirectClaimVerb(neighbor.CanonicalVerb)
						if sameVerb || bothExternal {
							sameVerbSkipped++
							log.Debug().
								Int64("fact_a", fact.ID).
								Int64("fact_b", neighbor.ID).
								Str("verb_a", fact.CanonicalVerb).
								Str("verb_b", neighbor.CanonicalVerb).
								Bool("both_external", bothExternal).
								Float64("similarity", 1.0-neighbor.Distance).
								Msg("Skipping paraphrase pair")
							continue
						}
					}
					pairsToCheck = append(pairsToCheck, neighborCandidate{
						ID: neighbor.ID, Text: neighbor.Text, PublishedAt: neighbor.PublishedAt,
						Similarity: 1.0 - neighbor.Distance})
				}
			}
		}

		for _, other := range pairsToCheck {
			targetID := other.ID
			targetText := other.Text
			targetDate := other.PublishedAt

			ancestorID, err := storer.GetMostGeneralAncestor(ctx, other.ID)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", other.ID).Msg("Failed to get ancestor, using original")
			} else if ancestorID != other.ID {
				var ancestorTx pgx.Tx
				if readTx != nil {
					ancestorTx = readTx
				} else {
					ancestorTx2, txErr := storer.Pool().Begin(ctx)
					if txErr == nil {
						ancestorTx = ancestorTx2
						defer func() { _ = ancestorTx2.Rollback(ctx) }()
					}
				}
				if ancestorTx != nil {
					ancestorText, err := storer.GetFactTextByID(ctx, ancestorTx, ancestorID)
					if err == nil {
						targetID = ancestorID
						targetText = ancestorText
						ancestorDate, err := storer.GetFactDateByID(ctx, ancestorTx, ancestorID)
						if err == nil {
							targetDate = ancestorDate
						}
					}
				}
			}

			allPairs = append(allPairs, pairInfo{
				factAID:    fact.ID,
				factAText:  fact.Text,
				factADate:  fact.PublishedAt,
				factBID:    targetID,
				factBText:  targetText,
				factBDate:  targetDate,
				similarity: other.Similarity,
			})
		}
	}

	if readTx != nil {
		_ = readTx.Rollback(ctx)
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

	// Deduplicate pairs and remove self-pairs
	{
		seen := make(map[string]int, len(allPairs))
		dedupedPairs := make([]pairInfo, 0, len(allPairs))
		selfPairs := 0
		for _, pair := range allPairs {
			if pair.factAID == pair.factBID {
				selfPairs++
				continue
			}
			idA, idB := pair.factAID, pair.factBID
			if idA > idB {
				idA, idB = idB, idA
			}
			key := fmt.Sprintf("%d-%d", idA, idB)
			if idx, exists := seen[key]; !exists {
				seen[key] = len(dedupedPairs)
				dedupedPairs = append(dedupedPairs, pair)
			} else if pair.queued {
				dedupedPairs[idx].queued = true
			}
		}
		removed := len(allPairs) - len(dedupedPairs)
		if removed > 0 {
			log.Info().Str("person", person.Name).
				Int("duplicate_pairs_removed", removed-selfPairs).
				Int("self_pairs_removed", selfPairs).
				Msg("Cleaned up pairs from ancestor walking")
		}
		allPairs = dedupedPairs
	}

	// Filter out pairs where neither fact has a direct claim verb
	{
		factVerbMap := make(map[int64]string, len(facts))
		for _, f := range facts {
			factVerbMap[f.ID] = f.CanonicalVerb
		}
		var directPairs []pairInfo
		externalPairsSkipped := 0
		for _, pair := range allPairs {
			verbA := factVerbMap[pair.factAID]
			verbB := factVerbMap[pair.factBID]
			if !pair.queued && !categorizer.IsDirectClaimVerb(verbA) && !categorizer.IsDirectClaimVerb(verbB) {
				externalPairsSkipped++
				continue
			}
			directPairs = append(directPairs, pair)
		}
		if externalPairsSkipped > 0 {
			log.Info().Str("person", person.Name).Int("skipped", externalPairsSkipped).Msg("Skipped external-only pairs (no direct claim verb)")
		}
		allPairs = directPairs
	}

	// Filter out pairs that have already been checked
	checkedPairs, err := storer.GetCheckedPairs(ctx, person.ID)
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Failed to get checked pairs")
		return nil
	}

	var filteredPairs []pairInfo
	for _, pair := range allPairs {
		idA, idB := pair.factAID, pair.factBID
		if idA > idB {
			idA, idB = idB, idA
		}
		pairKey := fmt.Sprintf("%d-%d", idA, idB)
		if !checkedPairs[pairKey] {
			filteredPairs = append(filteredPairs, pair)
		}
	}

	skipped := len(allPairs) - len(filteredPairs)
	if skipped > 0 {
		log.Info().Str("person", person.Name).Int("skipped", skipped).Msg("Skipped pairs with existing checks")
	}
	if sameVerbSkipped > 0 {
		log.Info().Str("person", person.Name).Int("skipped", sameVerbSkipped).Msg("Skipped same-verb pairs (paraphrases)")
	}

	log.Info().Str("person", person.Name).Int("pairs_to_check", len(filteredPairs)).Msg("Collected pairs for processing")
	return filteredPairs
}

// processSyncContradictions processes pairs synchronously via OpenRouter (original path).
func processSyncContradictions(ctx context.Context, storer *storage.PostgresStorer, allPersonPairs []personPairs, opts ContradictionDetectOpts) error {
	detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	// Bundler is best-effort: if either embedder or LLM is unavailable we still record
	// raw contradictions, just without the LLM-driven bundle resolution. Cross-stance
	// rows then stay as solo bundles until the next run.
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Embedding generator unavailable; new contradictions will not be bundled by LLM fallback")
	}
	var bundler *contradiction.Bundler
	if embedder != nil {
		bundler, err = contradiction.NewBundlerFromEnv(storer, embedder)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to construct contradiction Bundler; new contradictions will not be bundled by LLM fallback")
		}
	}

	// Best-effort dedup net for the "not a contradiction" path: a highly similar
	// non-contradictory pair may be the same claim restated (the embedding is
	// stance-blind by design, so the judge is the authority). If the adjudicator
	// confirms they are the same claim, we mark the pair a duplicate so it stops
	// generating future pairs/contradictions. Nil = disabled (no LLM env).
	dedupeJudge, err := core.NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Fact-dedupe adjudicator unavailable; contradiction-pass dedup disabled")
		dedupeJudge = nil
	}

	totalContradictions := 0
	totalPairsChecked := 0

	for _, pp := range allPersonPairs {
		personContradictions := 0
		batchSize := contradiction.MaxPairsPerBatchSync

		for i := 0; i < len(pp.pairs); i += batchSize {
			end := i + batchSize
			if end > len(pp.pairs) {
				end = len(pp.pairs)
			}
			batch := pp.pairs[i:end]

			factPairs := make([]contradiction.FactPair, len(batch))
			for j, p := range batch {
				factPairs[j] = contradiction.FactPair{
					IndexA: j,
					TextA:  p.factAText,
					DateA:  p.factADate,
					IndexB: j,
					TextB:  p.factBText,
					DateB:  p.factBDate,
				}
			}

			results := detector.AreContradictoryBatch(ctx, factPairs)
			totalPairsChecked += len(batch)

			var pairResults []storage.PairCheckResult
			for j, result := range results {
				if result.Error != nil {
					log.Error().Err(result.Error).Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Detector error")
					continue
				}

				if opts.Debug {
					fmt.Printf("\n[DEBUG] Fact A [%d]: %q\n", batch[j].factAID, batch[j].factAText)
					fmt.Printf("[DEBUG] Fact B [%d]: %q\n", batch[j].factBID, batch[j].factBText)
					reasoning := result.Reasoning
					if reasoning == "" {
						reasoning = result.RawResponse
					}
					fmt.Printf("[DEBUG] REASONING: %s\n", reasoning)
					verdict := "NO"
					if result.IsContradictory {
						verdict = "YES"
					}
					fmt.Printf("[DEBUG] VERDICT: %s — %s\n", verdict, result.Reason)
				}

				// Adversarial verification pass: refute each YES before recording.
				// On verifier error we fall open (keep the contradiction) and log;
				// on an explicit refutation we drop it and record the pair as
				// checked-and-not-contradictory so it is not re-detected.
				isContra := result.IsContradictory
				// Skip verification when either fact text is empty (e.g. a fact was
				// deleted mid-run) — we can't refute what we can't read, so keep
				// the contradiction (fail-open), mirroring the stance path.
				if isContra && batch[j].factAText != "" && batch[j].factBText != "" {
					keep, refutation, vErr := detector.VerifyContradiction(ctx, batch[j].factAText, batch[j].factBText, result.Reason)
					if vErr != nil {
						log.Warn().Err(vErr).
							Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
							Msg("Contradiction verification failed; keeping contradiction (fail-open)")
					} else if !keep {
						isContra = false
						log.Info().
							Str("person", pp.personName).
							Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
							Str("flagged_reason", result.Reason).
							Str("refutation", refutation).
							Msg("Contradiction refuted by verification pass; dropping")
					}
				}

				pairResults = append(pairResults, storage.PairCheckResult{
					FactAID:         batch[j].factAID,
					FactBID:         batch[j].factBID,
					IsContradiction: isContra,
				})

				// Dedup net: the judge said NOT a contradiction, but if the two
				// facts are near-identical they may be the same claim restated.
				// Ask the adjudicator and, if so, mark the pair a duplicate so the
				// redundant fact stops surfacing in future detection/dedup.
				if !isContra && dedupeJudge != nil && !opts.DryRun &&
					batch[j].similarity >= contradictionDedupSimFloor &&
					batch[j].factAText != "" && batch[j].factBText != "" {
					same, dErr := dedupeJudge.IsSameClaim(ctx, pp.personName, batch[j].factAText, batch[j].factBText)
					if dErr != nil {
						log.Warn().Err(dErr).
							Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
							Msg("Contradiction-pass dedup adjudication failed")
					} else if same {
						reason := fmt.Sprintf("contradiction-pass dedup: same claim (sim=%.2f)", batch[j].similarity)
						if mErr := storer.MarkFactDuplicate(ctx, batch[j].factAID, batch[j].factBID, reason); mErr != nil {
							log.Error().Err(mErr).
								Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
								Msg("Failed to mark facts as duplicates")
						} else {
							log.Info().
								Str("person", pp.personName).
								Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).
								Float64("similarity", batch[j].similarity).
								Msg("Marked facts as duplicates (contradiction-pass dedup)")
						}
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

					fmt.Printf("\n=== CONTRADICTION DETECTED ===\n")
					fmt.Printf("Person: %s\n", pp.personName)
					fmt.Printf("Fact A [%d]: %s\n", batch[j].factAID, batch[j].factAText)
					fmt.Printf("Fact B [%d]: %s\n", batch[j].factBID, batch[j].factBText)
					fmt.Printf("Reason: %s\n", result.Reason)

					if !opts.DryRun {
						contradictionID, needsBundler, err := storer.RecordContradictionImmediate(ctx, pp.personID, batch[j].factAID, batch[j].factBID, result.Reason, result.ReasonEn)
						if err != nil {
							log.Error().Err(err).Msg("Failed to record contradiction")
						} else {
							log.Info().Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Contradiction recorded and committed")
							if needsBundler && bundler != nil {
								if bErr := bundler.Resolve(ctx, pp.personID, contradictionID,
									batch[j].factAID, batch[j].factBID,
									batch[j].factAText, batch[j].factBText, result.Reason); bErr != nil {
									log.Warn().Err(bErr).Int64("contradiction_id", contradictionID).
										Msg("Bundler resolution failed; row remains a solo bundle")
								}
							}
						}
					}
				}
			}

			if !opts.DryRun && len(pairResults) > 0 {
				if err := storer.RecordPairChecksBatch(ctx, pp.personID, contradiction.JudgeRulebookHash(), pairResults); err != nil {
					log.Error().Err(err).Str("person", pp.personName).Msg("Failed to record pair checks")
				}
			}

			log.Debug().Int("batch_start", i).Int("batch_end", end).Int("total_pairs", len(pp.pairs)).Str("person", pp.personName).Msg("Batch processed")

			if end < len(pp.pairs) {
				time.Sleep(contradiction.DelayBetweenBatches)
			}
		}

		if personContradictions > 0 {
			log.Info().Str("person", pp.personName).Int("contradictions", personContradictions).Msg("Contradictions found and saved")
		}
	}

	fmt.Printf("\n=== SUMMARY ===\n")
	fmt.Printf("Total pairs checked: %d\n", totalPairsChecked)
	fmt.Printf("Total contradictions found: %d\n", totalContradictions)
	if opts.DryRun {
		fmt.Printf("(Dry run - nothing recorded to database)\n")
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

	return nil
}
