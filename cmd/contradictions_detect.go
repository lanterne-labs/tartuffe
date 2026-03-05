package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
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
	detectPersonFlag string
	detectDryRun     bool
	detectCheckAll   bool
	detectDebug      bool
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
	contradictionsDetectCmd.Flags().BoolVar(&detectDryRun, "dry-run", false, "Show detected contradictions without recording to database")
	contradictionsDetectCmd.Flags().BoolVar(&detectCheckAll, "check-all", false, "Check all fact pairs, not just semantically similar ones")
	contradictionsDetectCmd.Flags().BoolVar(&detectDebug, "debug", false, "Print LLM reasoning and verdict for every pair")
}

func runContradictionDetection(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	// Initialize storage
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	// Initialize contradiction detector
	detector, err := contradiction.NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create contradiction detector: %w", err)
	}

	// Get persons to process
	var persons []storage.PersonInfo
	if detectPersonFlag != "" {
		normalizedName := util.NormalizeString(detectPersonFlag)
		personID, err := storer.GetPersonIDByName(ctx, normalizedName)
		if err != nil {
			return fmt.Errorf("failed to find person %q: %w", detectPersonFlag, err)
		}
		persons = []storage.PersonInfo{{ID: personID, Name: normalizedName}}
		log.Info().Str("person", normalizedName).Int64("id", personID).Msg("Processing single person")
	} else {
		persons, err = storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("failed to get persons: %w", err)
		}
		log.Info().Int("count", len(persons)).Msg("Processing all persons")
	}

	totalContradictions := 0
	totalPairsChecked := 0

	for _, person := range persons {
		// Get all active (non-duplicate) facts for this person with embeddings
		facts, err := storer.GetFactsWithEmbeddingsForPerson(ctx, person.ID)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to get facts, skipping")
			continue
		}

		if len(facts) < 2 {
			log.Debug().Str("person", person.Name).Int("facts", len(facts)).Msg("Not enough facts to compare")
			continue
		}

		log.Info().Str("person", person.Name).Int("facts", len(facts)).Msg("Checking facts for contradictions")

		// Collect all pairs to check for this person
		var allPairs []pairInfo

		// Use a read-only transaction for neighbor queries (if not using check-all mode)
		var readTx pgx.Tx
		if !detectCheckAll {
			var txErr error
			readTx, txErr = storer.Pool().Begin(ctx)
			if txErr != nil {
				log.Error().Err(txErr).Str("person", person.Name).Msg("Failed to begin read transaction, skipping")
				continue
			}
		}

		sameVerbSkipped := 0

		for _, fact := range facts {
			var pairsToCheck []struct {
				ID          int64
				Text        string
				PublishedAt *time.Time
			}

			if detectCheckAll {
				// Check all pairs - compare with every fact that has higher ID
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
				// Use semantic similarity to find neighbors
				neighbors, err := storer.FindNeighborFactsForPerson(ctx, readTx, person.ID, fact.Embedding, fact.ID)
				if err != nil {
					log.Error().Err(err).Int64("fact_id", fact.ID).Msg("Failed to find neighbors")
					continue
				}
				for _, neighbor := range neighbors {
					if neighbor.ID > fact.ID {
						// Skip same-verb pairs with high similarity (>0.85) —
						// these are paraphrases, not contradictions
						if fact.CanonicalVerb != "" && neighbor.CanonicalVerb != "" &&
							fact.CanonicalVerb == neighbor.CanonicalVerb &&
							(1.0-neighbor.Distance) > 0.85 {
							sameVerbSkipped++
							log.Debug().
								Int64("fact_a", fact.ID).
								Int64("fact_b", neighbor.ID).
								Str("verb", fact.CanonicalVerb).
								Float64("similarity", 1.0-neighbor.Distance).
								Msg("Skipping same-verb pair")
							continue
						}
						pairsToCheck = append(pairsToCheck, struct {
							ID          int64
							Text        string
							PublishedAt *time.Time
						}{neighbor.ID, neighbor.Text, neighbor.PublishedAt})
					}
				}
			}

			// Walk UP implication chains for each pair target and build pairs
			for _, other := range pairsToCheck {
				targetID := other.ID
				targetText := other.Text
				targetDate := other.PublishedAt

				// Walk up the implication chain
				ancestorID, err := storer.GetMostGeneralAncestor(ctx, other.ID)
				if err != nil {
					log.Warn().Err(err).Int64("fact_id", other.ID).Msg("Failed to get ancestor, using original")
				} else if ancestorID != other.ID {
					// Fetch ancestor text - need a tx for this
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

		// Close read transaction
		if readTx != nil {
			_ = readTx.Rollback(ctx)
		}

		// Deduplicate pairs (ancestor walking can map multiple neighbors to the same ancestor)
		{
			seen := make(map[string]struct{}, len(allPairs))
			dedupedPairs := make([]pairInfo, 0, len(allPairs))
			for _, pair := range allPairs {
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
			if dupes := len(allPairs) - len(dedupedPairs); dupes > 0 {
				log.Info().Str("person", person.Name).Int("duplicate_pairs_removed", dupes).Msg("Deduplicated pairs from ancestor walking")
			}
			allPairs = dedupedPairs
		}

		// Get ALL checked pairs to avoid re-checking
		checkedPairs, err := storer.GetCheckedPairs(ctx, person.ID)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to get checked pairs")
			continue
		}

		// Filter out pairs that have already been checked
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
		allPairs = filteredPairs

		log.Info().Str("person", person.Name).Int("pairs_to_check", len(allPairs)).Msg("Collected pairs for batch processing")

		// Process pairs in batches
		personContradictions := 0
		batchSize := contradiction.MaxPairsPerBatch

		for i := 0; i < len(allPairs); i += batchSize {
			end := i + batchSize
			if end > len(allPairs) {
				end = len(allPairs)
			}
			batch := allPairs[i:end]

			// Convert to FactPair slice for the detector
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

			// Call batch detection
			results := detector.AreContradictoryBatch(ctx, factPairs)
			totalPairsChecked += len(batch)

			// Process results and collect pair check results
			var pairResults []storage.PairCheckResult
			for j, result := range results {
				if result.Error != nil {
					log.Error().Err(result.Error).Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Detector error")
					continue
				}

				// Print debug info for every pair when --debug is set
				if detectDebug {
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

				// Record the pair check (using the walked-up IDs)
				pairResults = append(pairResults, storage.PairCheckResult{
					FactAID:         batch[j].factAID,
					FactBID:         batch[j].factBID,
					IsContradiction: result.IsContradictory,
				})

				if result.IsContradictory {
					totalContradictions++
					personContradictions++

					log.Warn().
						Str("person", person.Name).
						Int64("fact_a", batch[j].factAID).
						Int64("fact_b", batch[j].factBID).
						Str("reason", result.Reason).
						Msg("Contradiction detected")

					fmt.Printf("\n=== CONTRADICTION DETECTED ===\n")
					fmt.Printf("Person: %s\n", person.Name)
					fmt.Printf("Fact A [%d]: %s\n", batch[j].factAID, batch[j].factAText)
					fmt.Printf("Fact B [%d]: %s\n", batch[j].factBID, batch[j].factBText)
					fmt.Printf("Reason: %s\n", result.Reason)

					if !detectDryRun {
						if err := storer.RecordContradictionImmediate(ctx, person.ID, batch[j].factAID, batch[j].factBID, result.Reason); err != nil {
							log.Error().Err(err).Msg("Failed to record contradiction")
						} else {
							log.Info().Int64("fact_a", batch[j].factAID).Int64("fact_b", batch[j].factBID).Msg("Contradiction recorded and committed")
						}
					}
				}
			}

			// Record all pair checks (both contradictions and non-contradictions)
			if !detectDryRun && len(pairResults) > 0 {
				if err := storer.RecordPairChecksBatch(ctx, person.ID, pairResults); err != nil {
					log.Error().Err(err).Str("person", person.Name).Msg("Failed to record pair checks")
				}
			}

			// Log batch progress
			log.Debug().Int("batch_start", i).Int("batch_end", end).Int("total_pairs", len(allPairs)).Str("person", person.Name).Msg("Batch processed")

			// Rate limiting
			if end < len(allPairs) {
				time.Sleep(contradiction.DelayBetweenBatches)
			}
		}

		// Log summary for this person
		if personContradictions > 0 {
			log.Info().Str("person", person.Name).Int("contradictions", personContradictions).Msg("Contradictions found and saved")
		}
	}

	fmt.Printf("\n=== SUMMARY ===\n")
	fmt.Printf("Total pairs checked: %d\n", totalPairsChecked)
	fmt.Printf("Total contradictions found: %d\n", totalContradictions)
	if detectDryRun {
		fmt.Printf("(Dry run - nothing recorded to database)\n")
	}

	// Record job metrics (skip in dry-run mode)
	if !detectDryRun {
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
