package cmd

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

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
	case "":
		return nil, fmt.Errorf("BATCH_PROVIDER env var not set (supported: gemini)")
	default:
		return nil, fmt.Errorf("unsupported BATCH_PROVIDER: %s (supported: gemini)", providerName)
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
