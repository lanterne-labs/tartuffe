package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var batchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Commands for LLM batch processing",
	Long: `Submit, monitor, and retrieve LLM batch jobs for fact extraction.

Batch processing is ~50% cheaper than synchronous API calls and is suitable
for processing large volumes of cached content.

Requires BATCH_PROVIDER and provider-specific env vars (e.g., GEMINI_API_KEY).

Examples:
  # Submit cached content for batch extraction
  ./dm batch submit --dir ./scraped_content/news_cache --target "Marine Le Pen"

  # Check status of a specific batch
  ./dm batch status 42

  # Retrieve and process results for a completed batch
  ./dm batch retrieve 42

  # List all batches
  ./dm batch list`,
}

var batchSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit cached content for batch fact extraction",
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		target, _ := cmd.Flags().GetString("target")
		targetType, _ := cmd.Flags().GetString("target-type")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if dir == "" || target == "" {
			return fmt.Errorf("--dir and --target are required")
		}

		ctx := context.Background()

		// Load content from directory
		items, err := core.LoadBatchContentFromDir(dir, target, targetType)
		if err != nil {
			return fmt.Errorf("failed to load content: %w", err)
		}

		log.Info().Int("items", len(items)).Str("target", target).Msg("Loaded content items")

		if dryRun {
			fmt.Printf("DRY RUN: Would submit %d content items (%d after chunking may vary)\n", len(items), len(items))
			for i, item := range items {
				fmt.Printf("  %d. %s (%d chars)\n", i+1, item.SourceURL, len(item.Content))
			}
			return nil
		}

		provider, err := newBatchProvider()
		if err != nil {
			return err
		}

		storer, err := storage.NewPostgresStorerFromEnv(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer storer.Close()

		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create embedder: %w", err)
		}

		inserter := core.NewFactInserter(storer)
		orchestrator := core.NewBatchJobOrchestrator(provider, newBatchStorerAdapter(storer), embedder, inserter, categorizer.ParseFactJSON)

		metadata := map[string]any{
			"target":      target,
			"target_type": targetType,
			"source_dir":  dir,
		}

		batchID, err := orchestrator.PrepareAndSubmit(ctx, items, metadata)
		if err != nil {
			return fmt.Errorf("failed to submit batch: %w", err)
		}

		fmt.Printf("Batch submitted: ID=%d\n", batchID)
		return nil
	},
}

var batchStatusCmd = &cobra.Command{
	Use:   "status [batch-id]",
	Short: "Check batch status (no id = show all pending)",
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		provider, err := newBatchProvider()
		if err != nil {
			return err
		}

		storer, err := storage.NewPostgresStorerFromEnv(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer storer.Close()

		adapter := newBatchStorerAdapter(storer)
		orchestrator := core.NewBatchJobOrchestrator(provider, adapter, nil, nil, nil)

		if len(args) > 0 {
			batchID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid batch ID: %w", err)
			}

			status, err := orchestrator.CheckStatus(ctx, batchID)
			if err != nil {
				return fmt.Errorf("failed to check status: %w", err)
			}

			fmt.Printf("Batch %d: state=%s completed=%d failed=%d\n",
				batchID, status.State, status.CompletedCount, status.FailedCount)
			if status.ErrorMessage != "" {
				fmt.Printf("  Error: %s\n", status.ErrorMessage)
			}
			return nil
		}

		// Show all pending/in_progress batches
		for _, state := range []string{"pending", "in_progress"} {
			batches, err := adapter.ListBatches(ctx, state)
			if err != nil {
				return fmt.Errorf("failed to list batches: %w", err)
			}
			for _, b := range batches {
				status, err := orchestrator.CheckStatus(ctx, b.ID)
				if err != nil {
					log.Warn().Err(err).Int64("batch_id", b.ID).Msg("Failed to check status")
					continue
				}
				fmt.Printf("Batch %d: state=%s completed=%d failed=%d\n",
					b.ID, status.State, status.CompletedCount, status.FailedCount)
			}
		}
		return nil
	},
}

var batchRetrieveCmd = &cobra.Command{
	Use:   "retrieve [batch-id]",
	Short: "Retrieve and process batch results (no id = all completed)",
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		provider, err := newBatchProvider()
		if err != nil {
			return err
		}

		storer, err := storage.NewPostgresStorerFromEnv(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer storer.Close()

		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create embedder: %w", err)
		}

		inserter := core.NewFactInserter(storer)
		adapter := newBatchStorerAdapter(storer)
		orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)

		var batchIDs []int64

		if len(args) > 0 {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid batch ID: %w", err)
			}
			batchIDs = append(batchIDs, id)
		} else {
			// Find all completed but unretrieved batches
			batches, err := adapter.ListBatches(ctx, "completed")
			if err != nil {
				return fmt.Errorf("failed to list batches: %w", err)
			}
			for _, b := range batches {
				if b.RetrievedAt == nil {
					batchIDs = append(batchIDs, b.ID)
				}
			}
		}

		if len(batchIDs) == 0 {
			fmt.Println("No completed batches to retrieve.")
			return nil
		}

		for _, id := range batchIDs {
			fmt.Printf("Retrieving batch %d...\n", id)
			result, err := orchestrator.RetrieveAndProcess(ctx, id)
			if err != nil {
				log.Error().Err(err).Int64("batch_id", id).Msg("Failed to retrieve batch")
				continue
			}
			fmt.Printf("  Processed: %d, Inserted: %d, Skipped: %d, Errors: %d\n",
				result.Processed, result.Inserted, result.Skipped, result.Errors)
		}
		return nil
	},
}

var batchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all batches",
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		storer, err := storage.NewPostgresStorerFromEnv(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer storer.Close()

		batches, err := storer.ListBatches(ctx, "")
		if err != nil {
			return fmt.Errorf("failed to list batches: %w", err)
		}

		if len(batches) == 0 {
			fmt.Println("No batches found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tPROVIDER\tSTATE\tREQUESTS\tCOMPLETED\tFAILED\tSUBMITTED\tRETRIEVED")
		for _, b := range batches {
			retrieved := "-"
			if b.RetrievedAt != nil {
				retrieved = b.RetrievedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
				b.ID, b.Provider, b.State, b.RequestCount,
				b.CompletedCount, b.FailedCount,
				b.SubmittedAt.Format(time.RFC3339), retrieved)
		}
		w.Flush()
		return nil
	},
}

var batchRecoverCmd = &cobra.Command{
	Use:   "recover [provider-batch-id]",
	Short: "Recover an orphaned batch that completed on the provider but has no DB records",
	Args:  cobra.ExactArgs(1),
	Annotations: map[string]string{"needs_db": "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		providerBatchID := args[0]
		ctx := context.Background()

		provider, err := newBatchProvider()
		if err != nil {
			return err
		}

		storer, err := storage.NewPostgresStorerFromEnv(ctx)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer storer.Close()

		// Step 1: Retrieve response items from the provider
		log.Info().Str("provider_batch_id", providerBatchID).Msg("Retrieving batch results from provider")
		responseItems, err := provider.Retrieve(ctx, providerBatchID)
		if err != nil {
			return fmt.Errorf("failed to retrieve batch from provider: %w", err)
		}
		log.Info().Int("response_items", len(responseItems)).Msg("Retrieved response items from provider")

		// Step 2: Check if a DB batch record already exists
		var batchID int64
		existingBatch, err := storer.GetBatchByProviderID(ctx, providerBatchID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("failed to look up batch in DB: %w", err)
			}

			// No DB record — create one
			log.Info().Msg("No DB batch record found, creating one")
			adapter := newBatchStorerAdapter(storer)
			batchID, err = adapter.CreateBatch(ctx, core.BatchCreateInput{
				Provider:        provider.ProviderName(),
				ProviderBatchID: providerBatchID,
				JobType:         "fact_extraction",
				RequestCount:    len(responseItems),
				Metadata: map[string]any{
					"recovered": true,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to create batch record: %w", err)
			}

			// Set state to completed so RetrieveAndProcess accepts it
			now := time.Now()
			if err := storer.UpdateBatchStatus(ctx, batchID, storage.BatchStatusUpdate{
				State:          "completed",
				CompletedAt:    &now,
			}); err != nil {
				return fmt.Errorf("failed to update batch state to completed: %w", err)
			}

			log.Info().Int64("batch_id", batchID).Msg("Created batch record with state=completed")
		} else {
			batchID = existingBatch.ID
			log.Info().Int64("batch_id", batchID).Str("state", existingBatch.State).Msg("Found existing batch record in DB")

			// If batch is not in completed state, update it
			if existingBatch.State != "completed" {
				now := time.Now()
				if err := storer.UpdateBatchStatus(ctx, batchID, storage.BatchStatusUpdate{
					State:       "completed",
					CompletedAt: &now,
				}); err != nil {
					return fmt.Errorf("failed to update batch state to completed: %w", err)
				}
				log.Info().Int64("batch_id", batchID).Msg("Updated batch state to completed")
			}
		}

		// Step 3: Reset request records — delete any partial/stale records and recreate from provider response
		adapter := newBatchStorerAdapter(storer)
		existingCount, err := storer.CountBatchRequests(ctx, batchID)
		if err != nil {
			return fmt.Errorf("failed to count existing requests: %w", err)
		}

		if existingCount > 0 && existingCount < len(responseItems) {
			// Partial state from a previous failed attempt — clean up
			log.Warn().Int("existing", existingCount).Int("expected", len(responseItems)).Msg("Partial request records found, deleting for clean recovery")
			if err := storer.DeleteBatchRequests(ctx, batchID); err != nil {
				return fmt.Errorf("failed to delete partial request records: %w", err)
			}
			existingCount = 0
		}

		if existingCount == 0 {
			// Dedup response items by customID (same fix as PrepareAndSubmit)
			seen := make(map[string]bool, len(responseItems))
			var reqInputs []core.BatchCreateRequestInput
			for _, item := range responseItems {
				if seen[item.CustomID] {
					log.Warn().Str("custom_id", item.CustomID).Msg("Duplicate response item skipped during recovery")
					continue
				}
				seen[item.CustomID] = true
				reqInputs = append(reqInputs, core.BatchCreateRequestInput{
					CustomID:   item.CustomID,
					TargetName: "recovered",
					TargetType: "person",
					ChunkIndex: 0,
				})
			}
			log.Info().Int("count", len(reqInputs)).Int("deduped_from", len(responseItems)).Msg("Creating request records from response items")
			if err := adapter.CreateBatchRequests(ctx, batchID, reqInputs); err != nil {
				return fmt.Errorf("failed to create request records: %w", err)
			}
			log.Info().Int("count", len(reqInputs)).Msg("Created request records")
		} else {
			log.Info().Int("count", existingCount).Msg("Request records already exist, skipping creation")
		}

		// Step 4: Set up the full orchestrator and process
		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create embedder: %w", err)
		}

		inserter := core.NewFactInserter(storer)
		orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)
		orchestrator.SetContradictionHandler(buildContradictionHandler(storer))

		fmt.Printf("Processing recovered batch %d (%d response items)...\n", batchID, len(responseItems))
		result, err := orchestrator.RetrieveAndProcess(ctx, batchID)
		if err != nil {
			return fmt.Errorf("failed to process batch: %w", err)
		}

		fmt.Printf("Recovery complete:\n")
		fmt.Printf("  Batch ID:  %d\n", batchID)
		fmt.Printf("  Processed: %d\n", result.Processed)
		fmt.Printf("  Inserted:  %d\n", result.Inserted)
		fmt.Printf("  Skipped:   %d\n", result.Skipped)
		fmt.Printf("  Errors:    %d\n", result.Errors)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(batchCmd)

	batchCmd.AddCommand(batchSubmitCmd)
	batchSubmitCmd.Flags().String("dir", "", "Directory containing cached JSON content files")
	batchSubmitCmd.Flags().String("target", "", "Target person/party name")
	batchSubmitCmd.Flags().String("target-type", "person", "Target type: person or party")
	batchSubmitCmd.Flags().Bool("dry-run", false, "Show what would be submitted without actually submitting")

	batchCmd.AddCommand(batchStatusCmd)
	batchCmd.AddCommand(batchRetrieveCmd)
	batchCmd.AddCommand(batchListCmd)
	batchCmd.AddCommand(batchRecoverCmd)
}

// newBatchProvider creates a BatchProvider from environment variables.
func newBatchProvider() (llm.BatchProvider, error) {
	providerName := os.Getenv("BATCH_PROVIDER")
	switch providerName {
	case "gemini":
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is required when BATCH_PROVIDER=gemini")
		}
		model := os.Getenv("GEMINI_BATCH_MODEL")
		return llm.NewGeminiBatchProvider(apiKey, model), nil
	case "mistral":
		apiKey := os.Getenv("MISTRAL_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("MISTRAL_API_KEY is required when BATCH_PROVIDER=mistral")
		}
		model := os.Getenv("MISTRAL_BATCH_MODEL")
		return llm.NewMistralBatchProvider(apiKey, model), nil
	case "":
		return nil, fmt.Errorf("BATCH_PROVIDER env var not set (supported: gemini, mistral)")
	default:
		return nil, fmt.Errorf("unsupported BATCH_PROVIDER: %s (supported: gemini, mistral)", providerName)
	}
}

// batchStorerAdapter bridges storage.PostgresStorer to core.BatchStorer interface.
type batchStorerAdapter struct {
	s *storage.PostgresStorer
}

func newBatchStorerAdapter(s *storage.PostgresStorer) *batchStorerAdapter {
	return &batchStorerAdapter{s: s}
}

func (a *batchStorerAdapter) CreateBatch(ctx context.Context, input core.BatchCreateInput) (int64, error) {
	return a.s.CreateBatch(ctx, storage.CreateBatchInput{
		Provider:        input.Provider,
		ProviderBatchID: input.ProviderBatchID,
		JobType:         input.JobType,
		RequestCount:    input.RequestCount,
		Metadata:        input.Metadata,
	})
}

func (a *batchStorerAdapter) CreateBatchRequests(ctx context.Context, batchID int64, inputs []core.BatchCreateRequestInput) error {
	storageInputs := make([]storage.CreateBatchRequestInput, len(inputs))
	for i, inp := range inputs {
		storageInputs[i] = storage.CreateBatchRequestInput{
			CustomID:    inp.CustomID,
			SourceURL:   inp.SourceURL,
			TargetName:  inp.TargetName,
			TargetType:  inp.TargetType,
			ChunkIndex:  inp.ChunkIndex,
			PublishedAt: inp.PublishedAt,
		}
	}
	return a.s.CreateBatchRequests(ctx, batchID, storageInputs)
}

func (a *batchStorerAdapter) GetBatch(ctx context.Context, batchID int64) (*core.BatchRecord, error) {
	b, err := a.s.GetBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	return convertBatch(b), nil
}

func (a *batchStorerAdapter) UpdateBatchStatus(ctx context.Context, batchID int64, update core.BatchStatusUpdateInput) error {
	return a.s.UpdateBatchStatus(ctx, batchID, storage.BatchStatusUpdate{
		State:          update.State,
		CompletedCount: update.CompletedCount,
		FailedCount:    update.FailedCount,
		ErrorMessage:   update.ErrorMessage,
		CompletedAt:    update.CompletedAt,
		RetrievedAt:    update.RetrievedAt,
	})
}

func (a *batchStorerAdapter) UpdateBatchProviderID(ctx context.Context, batchID int64, providerBatchID string) error {
	return a.s.UpdateBatchProviderID(ctx, batchID, providerBatchID)
}

func (a *batchStorerAdapter) GetUnprocessedRequests(ctx context.Context, batchID int64) ([]core.BatchRequestRecord, error) {
	reqs, err := a.s.GetUnprocessedRequests(ctx, batchID)
	if err != nil {
		return nil, err
	}
	result := make([]core.BatchRequestRecord, len(reqs))
	for i, r := range reqs {
		result[i] = core.BatchRequestRecord{
			ID:           r.ID,
			BatchID:      r.BatchID,
			CustomID:     r.CustomID,
			SourceURL:    r.SourceURL,
			TargetName:   r.TargetName,
			TargetType:   r.TargetType,
			ChunkIndex:   r.ChunkIndex,
			PublishedAt:  r.PublishedAt,
			ResponseJSON: r.ResponseJSON,
			ErrorMessage: r.ErrorMessage,
			Processed:    r.Processed,
		}
	}
	return result, nil
}

func (a *batchStorerAdapter) MarkRequestProcessed(ctx context.Context, reqID int64, responseJSON *string, errMsg *string) error {
	return a.s.MarkRequestProcessed(ctx, reqID, responseJSON, errMsg)
}

func (a *batchStorerAdapter) CountUnfinishedBatchesByMetadata(ctx context.Context, jobType, metaKey, metaValue string) (int, error) {
	return a.s.CountUnfinishedBatchesByMetadata(ctx, jobType, metaKey, metaValue)
}

func (a *batchStorerAdapter) ListBatches(ctx context.Context, stateFilter string) ([]core.BatchRecord, error) {
	batches, err := a.s.ListBatches(ctx, stateFilter)
	if err != nil {
		return nil, err
	}
	result := make([]core.BatchRecord, len(batches))
	for i, b := range batches {
		result[i] = *convertBatch(&b)
	}
	return result, nil
}

func convertBatch(b *storage.LLMBatch) *core.BatchRecord {
	return &core.BatchRecord{
		ID:              b.ID,
		Provider:        b.Provider,
		ProviderBatchID: b.ProviderBatchID,
		State:           b.State,
		JobType:         b.JobType,
		RequestCount:    b.RequestCount,
		CompletedCount:  b.CompletedCount,
		FailedCount:     b.FailedCount,
		ErrorMessage:    b.ErrorMessage,
		SubmittedAt:     b.SubmittedAt,
		CompletedAt:     b.CompletedAt,
		RetrievedAt:     b.RetrievedAt,
		Metadata:        b.Metadata,
	}
}

// buildContradictionHandler creates a handler that processes contradiction batch results.
// It parses the custom ID to extract person/fact IDs, parses the LLM response for verdicts,
// and records contradictions and pair checks in the database.
func buildContradictionHandler(storer *storage.PostgresStorer) core.ContradictionResultHandler {
	return func(ctx context.Context, customID string, llmResponse string) error {
		personID, factAIDs, factBIDs, err := contradiction.ParseContradictionCustomID(customID)
		if err != nil {
			return fmt.Errorf("failed to parse custom ID: %w", err)
		}

		results := contradiction.ProcessContradictionResponse(llmResponse, len(factAIDs))

		var pairResults []storage.PairCheckResult
		for i, result := range results {
			if result.Error != nil {
				log.Warn().Err(result.Error).
					Int64("fact_a", factAIDs[i]).Int64("fact_b", factBIDs[i]).
					Msg("Contradiction batch: pair parse error")
				continue
			}

			pairResults = append(pairResults, storage.PairCheckResult{
				FactAID:         factAIDs[i],
				FactBID:         factBIDs[i],
				IsContradiction: result.IsContradictory,
			})

			if result.IsContradictory {
				log.Warn().
					Int64("person_id", personID).
					Int64("fact_a", factAIDs[i]).
					Int64("fact_b", factBIDs[i]).
					Str("reason", result.Reason).
					Msg("Contradiction detected (batch)")

				if err := storer.RecordContradictionImmediate(ctx, personID, factAIDs[i], factBIDs[i], result.Reason); err != nil {
					log.Error().Err(err).Msg("Failed to record contradiction from batch")
				}
			}
		}

		if len(pairResults) > 0 {
			if err := storer.RecordPairChecksBatch(ctx, personID, pairResults); err != nil {
				log.Error().Err(err).Int64("person_id", personID).Msg("Failed to record pair checks from batch")
			}
		}

		return nil
	}
}
