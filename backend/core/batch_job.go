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
	UpdateBatchProviderID(ctx context.Context, batchID int64, providerBatchID string) error
	GetUnprocessedRequests(ctx context.Context, batchID int64) ([]BatchRequestRecord, error)
	MarkRequestProcessed(ctx context.Context, reqID int64, responseJSON *string, errMsg *string) error
	ListBatches(ctx context.Context, stateFilter string) ([]BatchRecord, error)
	CountUnfinishedBatchesByMetadata(ctx context.Context, jobType, metaKey, metaValue string) (int, error)
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
	SourceURL          string
	Content            string
	TargetName         string
	TargetType         string
	PublishedAt        *time.Time
	PluginInstructions string // Optional plugin-specific instructions for the LLM prompt
}

// InsertedFactInfo holds metadata about a fact inserted from a batch.
// Used to chain classification batch after fact extraction.
type InsertedFactInfo struct {
	FactID      int64
	Text        string // NormalizedFactText
	Verb        string // CanonicalVerb
	Embedding   []float32
	PersonIDs   []int64
	PublishedAt *time.Time
}

// RetrieveResult contains the outcome of retrieving and processing batch results.
type RetrieveResult struct {
	Processed     int
	Inserted      int
	Skipped       int
	Errors        int
	InsertedFacts []InsertedFactInfo // Populated by fact_extraction batches for classification chaining
}

// ContradictionResultHandler processes contradiction batch results.
// Called for each request in a "contradiction" job type batch.
type ContradictionResultHandler func(ctx context.Context, customID string, llmResponse string) error

// BatchJobOrchestrator prepares batch requests, persists state, and processes results.
type BatchJobOrchestrator struct {
	provider               llm.BatchProvider
	storer                 BatchStorer
	embedder               EmbeddingGenerator
	inserter               *FactInserter
	factParser             FactParser
	contradictionHandler   ContradictionResultHandler
	contradictionSubmitter func(ctx context.Context) error // Submits contradiction batch after classification completes
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

// SetContradictionHandler sets the handler for processing contradiction batch results.
func (o *BatchJobOrchestrator) SetContradictionHandler(h ContradictionResultHandler) {
	o.contradictionHandler = h
}

// SetContradictionSubmitter sets the function that submits contradiction detection
// after all classification batches for a pipeline run have completed.
func (o *BatchJobOrchestrator) SetContradictionSubmitter(fn func(ctx context.Context) error) {
	o.contradictionSubmitter = fn
}

// PrepareAndSubmit reads content items, builds prompts, chunks large content,
// persists state to the database, then submits to the batch provider.
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
			TargetName:         item.TargetName,
			SourceURL:          item.SourceURL,
			PluginInstructions: item.PluginInstructions,
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

	// Deduplicate by customID — identical URL+target+chunk produces identical LLM output
	seen := make(map[string]bool, len(batchRequests))
	dedupedBatchRequests := make([]llm.BatchRequest, 0, len(batchRequests))
	dedupedDBRequests := make([]BatchCreateRequestInput, 0, len(dbRequests))
	for i, req := range batchRequests {
		if seen[req.CustomID] {
			log.Warn().Str("custom_id", req.CustomID).Msg("Duplicate batch request skipped")
			continue
		}
		seen[req.CustomID] = true
		dedupedBatchRequests = append(dedupedBatchRequests, req)
		dedupedDBRequests = append(dedupedDBRequests, dbRequests[i])
	}
	batchRequests = dedupedBatchRequests
	dbRequests = dedupedDBRequests

	log.Info().Int("requests", len(batchRequests)).Msg("Submitting batch to provider")

	// Step 1: Persist batch to DB with pending provider ID
	batchID, err := o.storer.CreateBatch(ctx, BatchCreateInput{
		Provider:        o.provider.ProviderName(),
		ProviderBatchID: "pending",
		JobType:         "fact_extraction",
		RequestCount:    len(batchRequests),
		Metadata:        metadata,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to persist batch: %w", err)
	}

	// Step 2: Persist batch requests to DB
	if err := o.storer.CreateBatchRequests(ctx, batchID, dbRequests); err != nil {
		return 0, fmt.Errorf("failed to persist batch requests: %w", err)
	}

	// Step 3: Submit to provider
	submitResult, err := o.provider.Submit(ctx, batchRequests)
	if err != nil {
		// Mark batch as failed in DB
		errMsg := fmt.Sprintf("provider submit failed: %v", err)
		if updateErr := o.storer.UpdateBatchStatus(ctx, batchID, BatchStatusUpdateInput{
			State:        "failed",
			ErrorMessage: &errMsg,
		}); updateErr != nil {
			log.Error().Err(updateErr).Int64("batch_id", batchID).Msg("Failed to mark batch as failed after provider submit error")
		}
		return 0, fmt.Errorf("provider submit failed: %w", err)
	}

	// Step 4: Update batch with real provider batch ID
	if err := o.storer.UpdateBatchProviderID(ctx, batchID, submitResult.ProviderBatchID); err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Failed to update provider batch ID")
	}

	log.Info().
		Int64("batch_id", batchID).
		Str("provider_batch_id", submitResult.ProviderBatchID).
		Int("requests", submitResult.RequestCount).
		Msg("Batch submitted and persisted")

	return batchID, nil
}

// MaxRequestsPerContradictionBatch is the maximum number of LLM requests per
// contradiction batch submitted to the provider. Gemini free/tier-1 has a 3M
// enqueued-token limit; contradiction prompts are heavy (~3-4K tokens each),
// so we cap at 400 requests per batch to stay well under the limit.
const MaxRequestsPerContradictionBatch = 400

// SubmitContradictionBatch submits pre-built contradiction batch requests to the provider.
// Large request sets are automatically split into chunks of MaxRequestsPerContradictionBatch
// to avoid hitting provider token-enqueue limits.
// Returns the first DB batch ID for tracking and the total number of batches created.
func (o *BatchJobOrchestrator) SubmitContradictionBatch(ctx context.Context, requests []llm.BatchRequest, metadata map[string]any) (int64, error) {
	if len(requests) == 0 {
		return 0, fmt.Errorf("no contradiction requests to submit")
	}

	// Split requests into chunks
	var chunks [][]llm.BatchRequest
	for i := 0; i < len(requests); i += MaxRequestsPerContradictionBatch {
		end := i + MaxRequestsPerContradictionBatch
		if end > len(requests) {
			end = len(requests)
		}
		chunks = append(chunks, requests[i:end])
	}

	log.Info().
		Int("total_requests", len(requests)).
		Int("chunks", len(chunks)).
		Int("max_per_chunk", MaxRequestsPerContradictionBatch).
		Msg("Submitting contradiction batch(es) to provider")

	var firstBatchID int64
	for chunkIdx, chunk := range chunks {
		batchID, err := o.submitSingleContradictionBatch(ctx, chunk, metadata, chunkIdx+1, len(chunks))
		if err != nil {
			return firstBatchID, fmt.Errorf("chunk %d/%d failed: %w", chunkIdx+1, len(chunks), err)
		}
		if chunkIdx == 0 {
			firstBatchID = batchID
		}
	}

	return firstBatchID, nil
}

// submitSingleContradictionBatch submits one chunk of contradiction requests.
func (o *BatchJobOrchestrator) submitSingleContradictionBatch(ctx context.Context, requests []llm.BatchRequest, metadata map[string]any, chunkNum, totalChunks int) (int64, error) {
	// Build DB request records
	var dbRequests []BatchCreateRequestInput
	for _, req := range requests {
		dbRequests = append(dbRequests, BatchCreateRequestInput{
			CustomID:   req.CustomID,
			TargetName: "contradiction",
			TargetType: "system",
		})
	}

	// Add chunk info to metadata
	chunkMeta := make(map[string]any)
	for k, v := range metadata {
		chunkMeta[k] = v
	}
	chunkMeta["chunk"] = fmt.Sprintf("%d/%d", chunkNum, totalChunks)

	// Step 1: Persist batch to DB with pending provider ID
	batchID, err := o.storer.CreateBatch(ctx, BatchCreateInput{
		Provider:        o.provider.ProviderName(),
		ProviderBatchID: "pending",
		JobType:         "contradiction",
		RequestCount:    len(requests),
		Metadata:        chunkMeta,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to persist batch: %w", err)
	}

	// Step 2: Persist batch requests to DB
	if err := o.storer.CreateBatchRequests(ctx, batchID, dbRequests); err != nil {
		return 0, fmt.Errorf("failed to persist batch requests: %w", err)
	}

	// Step 3: Submit to provider
	submitResult, err := o.provider.Submit(ctx, requests)
	if err != nil {
		errMsg := fmt.Sprintf("provider submit failed: %v", err)
		if updateErr := o.storer.UpdateBatchStatus(ctx, batchID, BatchStatusUpdateInput{
			State:        "failed",
			ErrorMessage: &errMsg,
		}); updateErr != nil {
			log.Error().Err(updateErr).Int64("batch_id", batchID).Msg("Failed to mark batch as failed after provider submit error")
		}
		return 0, fmt.Errorf("provider submit failed: %w", err)
	}

	// Step 4: Update batch with real provider batch ID
	if err := o.storer.UpdateBatchProviderID(ctx, batchID, submitResult.ProviderBatchID); err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Failed to update provider batch ID")
	}

	log.Info().
		Int64("batch_id", batchID).
		Str("provider_batch_id", submitResult.ProviderBatchID).
		Int("requests", len(requests)).
		Int("chunk", chunkNum).
		Int("total_chunks", totalChunks).
		Msg("Contradiction batch chunk submitted and persisted")

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

	// Dispatch based on job type
	switch batch.JobType {
	case "contradiction":
		result, err = o.processContradictionBatch(ctx, unprocessed, responseMap)
		if err != nil {
			return result, err
		}
	case "classification":
		result, err = o.processClassificationBatch(ctx, unprocessed, responseMap)
		if err != nil {
			return result, err
		}
	default:
		result = o.processFactExtractionBatch(ctx, unprocessed, responseMap)
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
// Returns counts and metadata about inserted facts (for classification chaining).
func (o *BatchJobOrchestrator) insertFactsFromBatch(ctx context.Context, facts []backend.Fact, req BatchRequestRecord) (inserted, skipped int, insertedFacts []InsertedFactInfo, err error) {
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

		// Collect metadata for classification chaining
		for _, meta := range insertResult.InsertedMeta {
			insertedFacts = append(insertedFacts, InsertedFactInfo{
				FactID:      meta.FactID,
				Text:        fact.NormalizedFactText,
				Verb:        fact.CanonicalVerb,
				Embedding:   embeddings[0],
				PersonIDs:   []int64{meta.PersonID},
				PublishedAt: fact.PublishedAt,
			})
		}
	}

	return inserted, skipped, insertedFacts, nil
}

// processFactExtractionBatch handles retrieval for fact_extraction job type batches.
func (o *BatchJobOrchestrator) processFactExtractionBatch(ctx context.Context, unprocessed []BatchRequestRecord, responseMap map[string]*llm.BatchResponseItem) *RetrieveResult {
	result := &RetrieveResult{}

	for _, req := range unprocessed {
		select {
		case <-ctx.Done():
			return result
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

		inserted, skipped, batchFacts, err := o.insertFactsFromBatch(ctx, facts, req)
		if err != nil {
			errMsg := fmt.Sprintf("failed to insert facts: %v", err)
			log.Warn().Err(err).Str("custom_id", req.CustomID).Msg("Failed to insert facts from batch response")
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &jsonContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		if err := o.storer.MarkRequestProcessed(ctx, req.ID, &jsonContent, nil); err != nil {
			log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as processed")
		}

		result.Processed++
		result.Inserted += inserted
		result.Skipped += skipped
		result.InsertedFacts = append(result.InsertedFacts, batchFacts...)

		log.Debug().
			Str("custom_id", req.CustomID).
			Int("facts_parsed", len(facts)).
			Int("inserted", inserted).
			Int("skipped", skipped).
			Msg("Processed batch response")
	}

	return result
}

// processContradictionBatch handles retrieval for contradiction job type batches.
func (o *BatchJobOrchestrator) processContradictionBatch(ctx context.Context, unprocessed []BatchRequestRecord, responseMap map[string]*llm.BatchResponseItem) (*RetrieveResult, error) {
	result := &RetrieveResult{}

	if o.contradictionHandler == nil {
		return result, fmt.Errorf("no contradiction handler configured")
	}

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

		llmContent := respItem.Response.Choices[0].Message.Content

		if err := o.contradictionHandler(ctx, req.CustomID, llmContent); err != nil {
			errMsg := fmt.Sprintf("contradiction handler failed: %v", err)
			log.Warn().Err(err).Str("custom_id", req.CustomID).Msg("Failed to process contradiction batch response")
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &llmContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		if err := o.storer.MarkRequestProcessed(ctx, req.ID, &llmContent, nil); err != nil {
			log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as processed")
		}

		result.Processed++
		log.Debug().Str("custom_id", req.CustomID).Msg("Processed contradiction batch response")
	}

	return result, nil
}

// SubmitClassificationBatch builds and submits a classification batch for newly inserted facts.
// For each fact, finds similar existing facts and builds a classification prompt.
func (o *BatchJobOrchestrator) SubmitClassificationBatch(ctx context.Context, facts []InsertedFactInfo, parentBatchID int64) (int64, error) {
	if len(facts) == 0 {
		return 0, fmt.Errorf("no facts to classify")
	}

	// Access storer through inserter (same package)
	storer := o.inserter.storer

	// Similarity threshold for classification candidates (default 0.40 distance = 60% similarity)
	distanceThreshold := 0.40

	var batchRequests []llm.BatchRequest
	var dbRequests []BatchCreateRequestInput

	for _, fact := range facts {
		// Find similar existing facts
		similar, err := storer.FindSimilarActiveFacts(ctx, fact.PersonIDs, fact.Embedding, distanceThreshold, 20)
		if err != nil {
			log.Warn().Err(err).Int64("fact_id", fact.FactID).Msg("Classification: failed to find similar facts, skipping")
			continue
		}

		// Build classification inputs (exclude the new fact itself)
		var factsSection strings.Builder
		hasInputs := false
		for _, sf := range similar {
			if sf.ID == fact.FactID {
				continue
			}
			existingVerb, _ := storer.GetFactCanonicalVerb(ctx, sf.ID)
			fmt.Fprintf(&factsSection, "- ID: %d | Verb: %s | Text: %s | Published: unknown date\n",
				sf.ID, existingVerb, sf.Text)
			hasInputs = true
		}

		if !hasInputs {
			continue
		}

		pubDate := "unknown date"
		if fact.PublishedAt != nil {
			pubDate = fact.PublishedAt.Format("January 2, 2006")
		}

		prompt := fmt.Sprintf(classificationPrompt, fact.Verb, fact.Text, pubDate, factsSection.String())
		customID := fmt.Sprintf("cl:%d", fact.FactID)

		batchRequests = append(batchRequests, llm.BatchRequest{
			CustomID: customID,
			Messages: []llm.ChatMessage{
				{Role: "user", Content: prompt},
			},
			Temperature:    0.0,
			ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		})

		dbRequests = append(dbRequests, BatchCreateRequestInput{
			CustomID:   customID,
			TargetName: "classification",
			TargetType: "system",
		})
	}

	if len(batchRequests) == 0 {
		log.Info().Int("facts", len(facts)).Msg("No classification requests needed (no similar facts found)")
		return 0, nil
	}

	log.Info().Int("requests", len(batchRequests)).Msg("Submitting classification batch to provider")

	// Step 1: Persist batch to DB with pending provider ID
	batchID, err := o.storer.CreateBatch(ctx, BatchCreateInput{
		Provider:        o.provider.ProviderName(),
		ProviderBatchID: "pending",
		JobType:         "classification",
		RequestCount:    len(batchRequests),
		Metadata: map[string]any{
			"parent_extraction_batch_id": parentBatchID,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to persist batch: %w", err)
	}

	// Step 2: Persist batch requests to DB
	if err := o.storer.CreateBatchRequests(ctx, batchID, dbRequests); err != nil {
		return 0, fmt.Errorf("failed to persist batch requests: %w", err)
	}

	// Step 3: Submit to provider
	submitResult, err := o.provider.Submit(ctx, batchRequests)
	if err != nil {
		// Mark batch as failed in DB
		errMsg := fmt.Sprintf("provider submit failed: %v", err)
		if updateErr := o.storer.UpdateBatchStatus(ctx, batchID, BatchStatusUpdateInput{
			State:        "failed",
			ErrorMessage: &errMsg,
		}); updateErr != nil {
			log.Error().Err(updateErr).Int64("batch_id", batchID).Msg("Failed to mark batch as failed after provider submit error")
		}
		return 0, fmt.Errorf("provider submit failed: %w", err)
	}

	// Step 4: Update batch with real provider batch ID
	if err := o.storer.UpdateBatchProviderID(ctx, batchID, submitResult.ProviderBatchID); err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Failed to update provider batch ID")
	}

	log.Info().
		Int64("batch_id", batchID).
		Int64("parent_batch_id", parentBatchID).
		Int("requests", submitResult.RequestCount).
		Msg("Classification batch submitted and persisted")

	return batchID, nil
}

// processClassificationBatch handles retrieval for classification job type batches.
// Parses LLM classification responses and writes fact_relations rows.
func (o *BatchJobOrchestrator) processClassificationBatch(ctx context.Context, unprocessed []BatchRequestRecord, responseMap map[string]*llm.BatchResponseItem) (*RetrieveResult, error) {
	result := &RetrieveResult{}
	storer := o.inserter.storer

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

		llmContent := respItem.Response.Choices[0].Message.Content

		// Parse newFactID from custom ID: "cl:{factID}"
		var newFactID int64
		if _, err := fmt.Sscanf(req.CustomID, "cl:%d", &newFactID); err != nil {
			errMsg := fmt.Sprintf("failed to parse custom ID: %v", err)
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &llmContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		// Parse classification results
		classResults, err := ParseClassificationResponse(llmContent)
		if err != nil {
			errMsg := fmt.Sprintf("failed to parse classification: %v", err)
			log.Warn().Err(err).Str("custom_id", req.CustomID).Msg("Failed to parse classification response")
			if markErr := o.storer.MarkRequestProcessed(ctx, req.ID, &llmContent, &errMsg); markErr != nil {
				log.Warn().Err(markErr).Int64("req_id", req.ID).Msg("Failed to mark request as errored")
			}
			result.Errors++
			continue
		}

		// Convert to FactRelationInputs
		var relations []FactRelationInput
		for _, r := range classResults {
			var rel FactRelationInput
			rel.Reason = r.Reason

			switch r.Relation {
			case "duplicate":
				rel.GeneralFactID = r.ExistingFactID
				rel.SpecificFactID = newFactID
				rel.RelationType = "duplicate"
			case "implication_new_is_general":
				rel.GeneralFactID = newFactID
				rel.SpecificFactID = r.ExistingFactID
				rel.RelationType = "implication"
			case "implication_new_is_specific":
				rel.GeneralFactID = r.ExistingFactID
				rel.SpecificFactID = newFactID
				rel.RelationType = "implication"
			default:
				continue
			}

			relations = append(relations, rel)
		}

		if len(relations) > 0 {
			if err := storer.InsertFactRelationsBatch(ctx, relations); err != nil {
				log.Warn().Err(err).Int64("fact_id", newFactID).Msg("Failed to insert classification relations")
			} else {
				log.Info().Int64("fact_id", newFactID).Int("relations", len(relations)).Msg("Recorded classification relations from batch")
			}
		}

		if err := o.storer.MarkRequestProcessed(ctx, req.ID, &llmContent, nil); err != nil {
			log.Warn().Err(err).Int64("req_id", req.ID).Msg("Failed to mark request as processed")
		}

		result.Processed++
		result.Inserted += len(relations) // Reuse Inserted for relation count
	}

	return result, nil
}

// maybeSubmitContradictionBatch checks if all classification batches for a parent
// extraction batch are done. If so, triggers contradiction detection.
func (o *BatchJobOrchestrator) maybeSubmitContradictionBatch(ctx context.Context, batch *BatchRecord) {
	if o.contradictionSubmitter == nil {
		return
	}

	parentIDRaw, ok := batch.Metadata["parent_extraction_batch_id"]
	if !ok {
		return
	}
	parentID := fmt.Sprintf("%v", parentIDRaw)

	count, err := o.storer.CountUnfinishedBatchesByMetadata(ctx, "classification", "parent_extraction_batch_id", parentID)
	if err != nil {
		log.Error().Err(err).Str("parent_id", parentID).Msg("Failed to check pending classification batches")
		return
	}
	if count > 0 {
		log.Info().Int("pending", count).Str("parent_id", parentID).Msg("Classification batches still pending, deferring contradiction submission")
		return
	}

	log.Info().Str("parent_id", parentID).Msg("All classification batches done, submitting contradiction detection")
	if err := o.contradictionSubmitter(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to submit contradiction detection after classification")
	}
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
			// Skip batches that haven't been submitted to the provider yet
			if b.ProviderBatchID == "pending" {
				log.Debug().Int64("batch_id", b.ID).Msg("Batch poller: skipping batch with pending provider ID")
				continue
			}

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
	// Get batch info before processing (for chain logic)
	batch, err := o.storer.GetBatch(ctx, batchID)
	if err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Batch poller: failed to get batch info")
		return
	}

	result, err := o.RetrieveAndProcess(ctx, batchID)
	if err != nil {
		log.Error().Err(err).Int64("batch_id", batchID).Msg("Batch poller: failed to retrieve batch")
		return
	}
	log.Info().
		Int64("batch_id", batchID).
		Str("job_type", batch.JobType).
		Int("inserted", result.Inserted).
		Int("skipped", result.Skipped).
		Int("errors", result.Errors).
		Msg("Batch poller: auto-retrieved batch")

	// Chain: fact_extraction → classification
	if batch.JobType == "fact_extraction" && len(result.InsertedFacts) > 0 {
		clsBatchID, err := o.SubmitClassificationBatch(ctx, result.InsertedFacts, batchID)
		if err != nil {
			log.Error().Err(err).Int64("parent_batch_id", batchID).Msg("Failed to submit classification batch")
		} else if clsBatchID > 0 {
			log.Info().Int64("classification_batch_id", clsBatchID).Int64("parent_batch_id", batchID).Int("facts", len(result.InsertedFacts)).Msg("Classification batch submitted")
		}
	}

	// Chain: classification → contradiction
	if batch.JobType == "classification" {
		o.maybeSubmitContradictionBatch(ctx, batch)
	}
}
