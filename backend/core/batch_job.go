package core

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// FactParser parses raw LLM JSON output into Fact structs.
// This is injected to avoid an import cycle between core and categorizer.
// Wire categorizer.ParseFactJSON when constructing the orchestrator.
type FactParser func(jsonData []byte, sourceURL string, publishedAt *time.Time) ([]backend.Fact, error)

// BatchStorer defines the storage interface needed by BatchJobOrchestrator.
type BatchStorer interface {
	CreateBatch(ctx context.Context, input BatchCreateInput) (int64, error)
	CreateBatchRequests(ctx context.Context, batchID int64, inputs []BatchCreateRequestInput) error
	GetBatch(ctx context.Context, batchID int64) (*BatchRecord, error)
	UpdateBatchStatus(ctx context.Context, batchID int64, update BatchStatusUpdateInput) error
	GetUnprocessedRequests(ctx context.Context, batchID int64) ([]BatchRequestRecord, error)
	MarkRequestProcessed(ctx context.Context, reqID int64, responseJSON *string, errMsg *string) error
	ListBatches(ctx context.Context, stateFilter string) ([]BatchRecord, error)
}

// BatchCreateInput mirrors storage.CreateBatchInput to avoid import cycle.
type BatchCreateInput struct {
	Provider        string
	ProviderBatchID string
	JobType         string
	RequestCount    int
	Metadata        map[string]any
}

// BatchCreateRequestInput mirrors storage.CreateBatchRequestInput.
type BatchCreateRequestInput struct {
	CustomID    string
	SourceURL   *string
	TargetName  string
	TargetType  string
	ChunkIndex  int
	PublishedAt *time.Time
}

// BatchRecord mirrors storage.LLMBatch.
type BatchRecord struct {
	ID              int64
	Provider        string
	ProviderBatchID string
	State           string
	JobType         string
	RequestCount    int
	CompletedCount  int
	FailedCount     int
	ErrorMessage    *string
	SubmittedAt     time.Time
	CompletedAt     *time.Time
	RetrievedAt     *time.Time
	Metadata        map[string]any
}

// BatchRequestRecord mirrors storage.LLMBatchRequest.
type BatchRequestRecord struct {
	ID           int64
	BatchID      int64
	CustomID     string
	SourceURL    *string
	TargetName   string
	TargetType   string
	ChunkIndex   int
	PublishedAt  *time.Time
	ResponseJSON *string
	ErrorMessage *string
	Processed    bool
}

// BatchStatusUpdateInput mirrors storage.BatchStatusUpdate.
type BatchStatusUpdateInput struct {
	State          string
	CompletedCount *int
	FailedCount    *int
	ErrorMessage   *string
	CompletedAt    *time.Time
	RetrievedAt    *time.Time
}

// BatchContentItem represents a piece of content to be submitted for batch extraction.
type BatchContentItem struct {
	SourceURL   string
	Content     string
	TargetName  string
	TargetType  string
	PublishedAt *time.Time
}

// RetrieveResult contains the outcome of retrieving and processing batch results.
type RetrieveResult struct {
	Processed int
	Inserted  int
	Skipped   int
	Errors    int
}

// BatchJobOrchestrator prepares batch requests, persists state, and processes results.
type BatchJobOrchestrator struct {
	provider   llm.BatchProvider
	storer     BatchStorer
	embedder   EmbeddingGenerator
	inserter   *FactInserter
	factParser FactParser
}

// NewBatchJobOrchestrator creates a new orchestrator.
// factParser should be categorizer.ParseFactJSON (injected to avoid import cycle).
func NewBatchJobOrchestrator(
	provider llm.BatchProvider,
	storer BatchStorer,
	embedder EmbeddingGenerator,
	inserter *FactInserter,
	factParser FactParser,
) *BatchJobOrchestrator {
	return &BatchJobOrchestrator{
		provider:   provider,
		storer:     storer,
		embedder:   embedder,
		inserter:   inserter,
		factParser: factParser,
	}
}

// PrepareAndSubmit reads content items, builds prompts, chunks large content,
// submits to the batch provider, and persists state to the database.
func (o *BatchJobOrchestrator) PrepareAndSubmit(ctx context.Context, items []BatchContentItem, metadata map[string]any) (int64, error) {
	if len(items) == 0 {
		return 0, fmt.Errorf("no content items to submit")
	}

	var batchRequests []llm.BatchRequest
	var dbRequests []BatchCreateRequestInput

	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			log.Warn().Str("url", item.SourceURL).Msg("Empty content, skipping")
			continue
		}

		urlHash := shortHash(item.SourceURL + "|" + item.TargetName)
		targetType := item.TargetType
		if targetType == "" {
			targetType = "person"
		}

		cfg := FactExtractorConfig{
			TargetName: item.TargetName,
			SourceURL:  item.SourceURL,
		}

		var chunks []string
		if len(item.Content) > chunkingThreshold {
			chunks = util.ChunkText(item.Content, chunkSize, chunkOverlap)
		} else {
			chunks = []string{item.Content}
		}

		for i, chunk := range chunks {
			customID := fmt.Sprintf("%s:%d", urlHash, i)
			cfg.Content = chunk
			prompt := BuildFactExtractionPrompt(cfg)

			batchRequests = append(batchRequests, llm.BatchRequest{
				CustomID: customID,
				Messages: []llm.ChatMessage{
					{Role: "system", Content: prompt},
				},
				Temperature:    0.1,
				ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
			})

			srcURL := item.SourceURL
			dbRequests = append(dbRequests, BatchCreateRequestInput{
				CustomID:    customID,
				SourceURL:   &srcURL,
				TargetName:  item.TargetName,
				TargetType:  targetType,
				ChunkIndex:  i,
				PublishedAt: item.PublishedAt,
			})
		}
	}

	if len(batchRequests) == 0 {
		return 0, fmt.Errorf("no valid requests after processing content items")
	}

	log.Info().Int("requests", len(batchRequests)).Msg("Submitting batch to provider")

	// Submit to provider
	submitResult, err := o.provider.Submit(ctx, batchRequests)
	if err != nil {
		return 0, fmt.Errorf("provider submit failed: %w", err)
	}

	// Persist to DB
	batchID, err := o.storer.CreateBatch(ctx, BatchCreateInput{
		Provider:        o.provider.ProviderName(),
		ProviderBatchID: submitResult.ProviderBatchID,
		JobType:         "fact_extraction",
		RequestCount:    submitResult.RequestCount,
		Metadata:        metadata,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to persist batch: %w", err)
	}

	if err := o.storer.CreateBatchRequests(ctx, batchID, dbRequests); err != nil {
		return 0, fmt.Errorf("failed to persist batch requests: %w", err)
	}

	log.Info().
		Int64("batch_id", batchID).
		Str("provider_batch_id", submitResult.ProviderBatchID).
		Int("requests", submitResult.RequestCount).
		Msg("Batch submitted and persisted")

	return batchID, nil
}

// CheckStatus polls the provider for batch status and updates the DB.
func (o *BatchJobOrchestrator) CheckStatus(ctx context.Context, batchDBID int64) (*llm.BatchStatus, error) {
	batch, err := o.storer.GetBatch(ctx, batchDBID)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch: %w", err)
	}

	status, err := o.provider.Status(ctx, batch.ProviderBatchID)
	if err != nil {
		return nil, fmt.Errorf("failed to check status: %w", err)
	}

	update := BatchStatusUpdateInput{
		State: string(status.State),
	}
	if status.CompletedCount > 0 {
		update.CompletedCount = &status.CompletedCount
	}
	if status.FailedCount > 0 {
		update.FailedCount = &status.FailedCount
	}
	if status.ErrorMessage != "" {
		update.ErrorMessage = &status.ErrorMessage
	}
	if status.State == llm.BatchStateCompleted || status.State == llm.BatchStateFailed || status.State == llm.BatchStateExpired {
		now := time.Now()
		update.CompletedAt = &now
	}

	if err := o.storer.UpdateBatchStatus(ctx, batchDBID, update); err != nil {
		log.Warn().Err(err).Int64("batch_id", batchDBID).Msg("Failed to update batch status in DB")
	}

	return status, nil
}

// RetrieveAndProcess fetches results from the provider, parses facts, generates embeddings,
// and inserts them through the existing FactInserter pipeline.
func (o *BatchJobOrchestrator) RetrieveAndProcess(ctx context.Context, batchDBID int64) (*RetrieveResult, error) {
	batch, err := o.storer.GetBatch(ctx, batchDBID)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch: %w", err)
	}

	if batch.State != string(llm.BatchStateCompleted) {
		return nil, fmt.Errorf("batch %d is not completed (state: %s)", batchDBID, batch.State)
	}

	// Retrieve results from provider
	responseItems, err := o.provider.Retrieve(ctx, batch.ProviderBatchID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve results: %w", err)
	}

	// Build response map by customID for fast lookup
	responseMap := make(map[string]*llm.BatchResponseItem, len(responseItems))
	for i := range responseItems {
		responseMap[responseItems[i].CustomID] = &responseItems[i]
	}

	// Get unprocessed requests from DB
	unprocessed, err := o.storer.GetUnprocessedRequests(ctx, batchDBID)
	if err != nil {
		return nil, fmt.Errorf("failed to get unprocessed requests: %w", err)
	}

	result := &RetrieveResult{}

	for _, req := range unprocessed {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		respItem, ok := responseMap[req.CustomID]
		if !ok {
			errMsg := "no response found for request"
			if err := o.storer.MarkRequestProcessed(ctx, req.ID, nil, &errMsg); err != nil {
				log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		if respItem.Error != "" {
			errMsg := respItem.Error
			if err := o.storer.MarkRequestProcessed(ctx, req.ID, nil, &errMsg); err != nil {
				log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		if respItem.Response == nil || len(respItem.Response.Choices) == 0 {
			errMsg := "empty response"
			if err := o.storer.MarkRequestProcessed(ctx, req.ID, nil, &errMsg); err != nil {
				log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		jsonContent := respItem.Response.Choices[0].Message.Content

		// Parse facts from the LLM response
		sourceURL := ""
		if req.SourceURL != nil {
			sourceURL = *req.SourceURL
		}
		facts, err := o.factParser([]byte(jsonContent), sourceURL, req.PublishedAt)
		if err != nil {
			errMsg := fmt.Sprintf("failed to parse facts: %v", err)
			log.Warn().Err(err).Str("custom_id", req.CustomID).Msg("Failed to parse facts from batch response")
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &jsonContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		// Generate embeddings and insert facts
		inserted, skipped, err := o.insertFactsFromBatch(ctx, facts, req)
		if err != nil {
			errMsg := fmt.Sprintf("failed to insert facts: %v", err)
			log.Warn().Err(err).Str("custom_id", req.CustomID).Msg("Failed to insert facts from batch response")
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &jsonContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		// Mark as successfully processed
		if err := o.storer.MarkRequestProcessed(ctx, req.ID, &jsonContent, nil); err != nil {
			log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as processed")
		}

		result.Processed++
		result.Inserted += inserted
		result.Skipped += skipped

		log.Debug().
			Str("custom_id", req.CustomID).
			Int("facts_parsed", len(facts)).
			Int("inserted", inserted).
			Int("skipped", skipped).
			Msg("Processed batch response")
	}

	// Update batch as retrieved
	now := time.Now()
	if err := o.storer.UpdateBatchStatus(ctx, batchDBID, BatchStatusUpdateInput{
		RetrievedAt: &now,
	}); err != nil {
		log.Warn().Err(err).Int64("batch_id", batchDBID).Msg("Failed to mark batch as retrieved")
	}

	log.Info().
		Int64("batch_id", batchDBID).
		Int("processed", result.Processed).
		Int("inserted", result.Inserted).
		Int("skipped", result.Skipped).
		Int("errors", result.Errors).
		Msg("Batch retrieval complete")

	return result, nil
}

// insertFactsFromBatch generates embeddings and inserts facts through the FactInserter pipeline.
func (o *BatchJobOrchestrator) insertFactsFromBatch(ctx context.Context, facts []backend.Fact, req BatchRequestRecord) (inserted, skipped int, err error) {
	for _, fact := range facts {
		// Generate embedding from EmbeddingText
		embeddings, err := o.embedder.GenerateEmbeddings(ctx, []string{fact.EmbeddingText})
		if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
			log.Warn().Err(err).Str("fact", truncate(fact.NormalizedFactText, 80)).Msg("Failed to generate embedding, skipping fact")
			continue
		}

		input := StoreFactInput{
			SubjectEntity:      fact.SubjectEntity,
			SubjectEntityType:  fact.SubjectEntityType,
			NormalizedFactText: fact.NormalizedFactText,
			EmbeddingText:      fact.EmbeddingText,
			Embedding:          embeddings[0],
			OriginalQuote:      fact.OriginalQuote,
			CanonicalVerb:      fact.CanonicalVerb,
			InvolvedPeople:     fact.InvolvedPeople,
			SourceURL:          fact.SourceURL,
			PublishedAt:        fact.PublishedAt,
			SourceType:         "batch",
			Metadata: map[string]any{
				"batch_request_id": req.ID,
				"target_name":      req.TargetName,
				"target_type":      req.TargetType,
				"chunk_index":      req.ChunkIndex,
			},
		}

		insertResult, err := o.inserter.InsertFacts(ctx, []StoreFactInput{input})
		if err != nil {
			log.Warn().Err(err).Str("fact", truncate(fact.NormalizedFactText, 80)).Msg("Failed to store fact from batch")
			continue
		}

		inserted += insertResult.Inserted
		skipped += insertResult.Skipped
	}

	return inserted, skipped, nil
}

// LoadBatchContentFromDir reads cached JSON files from a directory and returns BatchContentItems.
func LoadBatchContentFromDir(dir, targetName, targetType string) ([]BatchContentItem, error) {
	if targetType == "" {
		targetType = "person"
	}

	// Check for target-specific subdirectory
	slug := strings.ToLower(strings.ReplaceAll(targetName, " ", "_"))
	subDir := filepath.Join(dir, slug)
	if info, err := os.Stat(subDir); err == nil && info.IsDir() {
		dir = subDir
		log.Info().Str("dir", dir).Msg("Using target-specific subdirectory")
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob directory: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no JSON files found in %s", dir)
	}

	var items []BatchContentItem
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn().Err(err).Str("file", path).Msg("Failed to read file, skipping")
			continue
		}

		var content struct {
			URL           string     `json:"url"`
			Content       string     `json:"content"`
			DatePublished *time.Time `json:"date_published,omitempty"`
		}
		if err := json.Unmarshal(data, &content); err != nil {
			log.Warn().Err(err).Str("file", path).Msg("Failed to parse JSON, skipping")
			continue
		}

		if strings.TrimSpace(content.Content) == "" {
			log.Warn().Str("file", path).Msg("Empty content, skipping")
			continue
		}

		items = append(items, BatchContentItem{
			SourceURL:   content.URL,
			Content:     content.Content,
			TargetName:  targetName,
			TargetType:  targetType,
			PublishedAt: content.DatePublished,
		})
	}

	log.Info().Int("files", len(items)).Str("dir", dir).Msg("Loaded content files for batch")
	return items, nil
}

// shortHash returns first 12 chars of SHA256 hex for a string.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:6])
}

// Chunking parameters matching openai_extractor.go
const (
	chunkingThreshold = 25000
	chunkSize         = 20000
	chunkOverlap      = 500
)

// StartBatchPoller runs a background loop that polls pending/in-progress batches,
// updates their status, and auto-retrieves completed ones.
// Runs immediately on startup (to catch batches that completed during downtime),
// then repeats on the given interval. Blocks until ctx is cancelled.
func StartBatchPoller(ctx context.Context, orchestrator *BatchJobOrchestrator, interval time.Duration) {
	log.Info().Dur("interval", interval).Msg("Starting batch poller")

	// Run immediately on startup to catch batches that completed while server was down
	pollBatches(ctx, orchestrator)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Batch poller stopped")
			return
		case <-ticker.C:
			pollBatches(ctx, orchestrator)
		}
	}
}

func pollBatches(ctx context.Context, o *BatchJobOrchestrator) {
	// Poll pending and in-progress batches for status updates
	for _, state := range []string{"pending", "in_progress"} {
		batches, err := o.storer.ListBatches(ctx, state)
		if err != nil {
			log.Error().Err(err).Str("state", state).Msg("Batch poller: failed to list batches")
			continue
		}

		for _, b := range batches {
			status, err := o.CheckStatus(ctx, b.ID)
			if err != nil {
				log.Error().Err(err).Int64("batch_id", b.ID).Msg("Batch poller: failed to check status")
				continue
			}

			log.Info().
				Int64("batch_id", b.ID).
				Str("state", string(status.State)).
				Msg("Batch poller: checked status")

			if status.State == llm.BatchStateCompleted {
				retrieveBatch(ctx, o, b.ID)
			}
		}
	}

	// Retrieve any completed batches that haven't been retrieved yet
	// (e.g. status was updated by a manual CLI call or a previous cycle that crashed mid-retrieve)
	completed, err := o.storer.ListBatches(ctx, "completed")
	if err != nil {
		log.Error().Err(err).Msg("Batch poller: failed to list completed batches")
		return
	}
	for _, b := range completed {
		if b.RetrievedAt == nil {
			retrieveBatch(ctx, o, b.ID)
		}
	}
}

func retrieveBatch(ctx context.Context, o *BatchJobOrchestrator, batchID int64) {
	result, err := o.RetrieveAndProcess(ctx, batchID)
	if err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Batch poller: failed to retrieve batch")
		return
	}
	log.Info().
		Int64("batch_id", batchID).
		Int("inserted", result.Inserted).
		Int("skipped", result.Skipped).
		Int("errors", result.Errors).
		Msg("Batch poller: auto-retrieved batch")
}
