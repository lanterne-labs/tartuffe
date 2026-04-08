package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"os"
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
// Called by both the CLI command and the pipeline scheduler.
// When BATCH_PROVIDER is set (and not in debug/dry-run mode), submits pairs to Gemini batch API.
// Otherwise, processes synchronously via OpenRouter.
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

	// Decide: batch (Gemini) or sync (OpenRouter)
	useBatch := os.Getenv("BATCH_PROVIDER") != "" && !opts.Debug && !opts.DryRun
	if useBatch {
		return submitContradictionBatch(ctx, storer, allPersonPairs, totalPairs)
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
		var pairsToCheck []struct {
			ID          int64
			Text        string
			PublishedAt *time.Time
		}

		if opts.CheckAll {
			for _, other := range facts {
				if other.ID > fact.ID {
					pairsToCheck = append(pairsToCheck, struct {
						ID          int64
						Text        string
						PublishedAt *time.Time
					}{other.ID, other.Text, other.PublishedAt})
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
						(1.0-neighbor.Distance) > 0.85 {
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
					pairsToCheck = append(pairsToCheck, struct {
						ID          int64
						Text        string
						PublishedAt *time.Time
					}{neighbor.ID, neighbor.Text, neighbor.PublishedAt})
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
				factAID:   fact.ID,
				factAText: fact.Text,
				factADate: fact.PublishedAt,
				factBID:   targetID,
				factBText: targetText,
				factBDate: targetDate,
			})
		}
	}

	if readTx != nil {
		_ = readTx.Rollback(ctx)
	}

	// Deduplicate pairs and remove self-pairs
	{
		seen := make(map[string]struct{}, len(allPairs))
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
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				dedupedPairs = append(dedupedPairs, pair)
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
			if !categorizer.IsDirectClaimVerb(verbA) && !categorizer.IsDirectClaimVerb(verbB) {
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

// submitContradictionBatch submits all collected pairs to the Gemini batch API.
// The batch poller will process results asynchronously.
func submitContradictionBatch(ctx context.Context, storer *storage.PostgresStorer, allPersonPairs []personPairs, totalPairs int) error {
	provider, err := newBatchProvider()
	if err != nil {
		return fmt.Errorf("failed to create batch provider: %w", err)
	}

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create embedder: %w", err)
	}

	inserter := core.NewFactInserter(storer)
	adapter := newBatchStorerAdapter(storer)
	orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)

	// Build all batch requests across all persons
	var allRequests []llm.BatchRequest
	for _, pp := range allPersonPairs {
		// Convert pairInfo to contradiction.FactPair + ID slices
		factPairs := make([]contradiction.FactPair, len(pp.pairs))
		factAIDs := make([]int64, len(pp.pairs))
		factBIDs := make([]int64, len(pp.pairs))
		for i, p := range pp.pairs {
			factPairs[i] = contradiction.FactPair{
				TextA: p.factAText,
				DateA: p.factADate,
				TextB: p.factBText,
				DateB: p.factBDate,
			}
			factAIDs[i] = p.factAID
			factBIDs[i] = p.factBID
		}

		requests := contradiction.PrepareContradictionBatch(pp.personID, factPairs, factAIDs, factBIDs)
		allRequests = append(allRequests, requests...)
	}

	if len(allRequests) == 0 {
		fmt.Printf("\n=== SUMMARY ===\nNo requests generated.\n")
		return nil
	}

	batchID, err := orchestrator.SubmitContradictionBatch(ctx, allRequests, map[string]any{
		"source":      "contradictions-detect",
		"total_pairs": totalPairs,
		"submitted":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("failed to submit contradiction batch: %w", err)
	}

	submitted := len(allRequests)
	if submitted > core.MaxRequestsPerContradictionBatch {
		submitted = core.MaxRequestsPerContradictionBatch
	}
	fmt.Printf("\n=== BATCH SUBMITTED ===\n")
	fmt.Printf("Batch ID: %d\n", batchID)
	fmt.Printf("Total pairs: %d\n", totalPairs)
	fmt.Printf("LLM requests: %d submitted (of %d total; rest deferred to next cycle)\n", submitted, len(allRequests))
	fmt.Printf("Results will be processed by the batch poller.\n")

	return nil
}

// processSyncContradictions processes pairs synchronously via OpenRouter (original path).
func processSyncContradictions(ctx context.Context, storer *storage.PostgresStorer, allPersonPairs []personPairs, opts ContradictionDetectOpts) error {
	detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	totalContradictions := 0
	totalPairsChecked := 0

	for _, pp := range allPersonPairs {
		personContradictions := 0
		batchSize := contradiction.MaxPairsPerBatch

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

				pairResults = append(pairResults, storage.PairCheckResult{
					FactAID:         batch[j].factAID,
					FactBID:         batch[j].factBID,
					IsContradiction: result.IsContradictory,
				})

				if result.IsContradictory {
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
						if err := storer.RecordContradictionImmediate(ctx, pp.personID, batch[j].factAID, batch[j].factBID, result.Reason); err != nil {
							log.Error().Err(err).Msg("Failed to record contradiction")
						} else {
							log.Info().Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Contradiction recorded and committed")
						}
					}
				}
			}

			if !opts.DryRun && len(pairResults) > 0 {
				if err := storer.RecordPairChecksBatch(ctx, pp.personID, pairResults); err != nil {
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
