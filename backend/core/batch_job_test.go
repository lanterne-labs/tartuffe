package core

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock: llm.BatchProvider
// ---------------------------------------------------------------------------

type mockBatchProvider struct {
	name string

	// Submit
	submitResult *llm.BatchSubmitResult
	submitErr    error
	submitCalls  [][]llm.BatchRequest

	// Status
	statusResult *llm.BatchStatus
	statusErr    error
	statusCalls  []string // provider batch IDs passed

	// Retrieve
	retrieveResult []llm.BatchResponseItem
	retrieveErr    error
	retrieveCalls  []string
}

func (m *mockBatchProvider) ProviderName() string { return m.name }

func (m *mockBatchProvider) Submit(ctx context.Context, requests []llm.BatchRequest) (*llm.BatchSubmitResult, error) {
	m.submitCalls = append(m.submitCalls, requests)
	if m.submitErr != nil {
		return nil, m.submitErr
	}
	if m.submitResult != nil {
		return m.submitResult, nil
	}
	// Default: echo back request count
	return &llm.BatchSubmitResult{
		ProviderBatchID: "provider-batch-123",
		RequestCount:    len(requests),
	}, nil
}

func (m *mockBatchProvider) Status(ctx context.Context, providerBatchID string) (*llm.BatchStatus, error) {
	m.statusCalls = append(m.statusCalls, providerBatchID)
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}

func (m *mockBatchProvider) Retrieve(ctx context.Context, providerBatchID string) ([]llm.BatchResponseItem, error) {
	m.retrieveCalls = append(m.retrieveCalls, providerBatchID)
	if m.retrieveErr != nil {
		return nil, m.retrieveErr
	}
	return m.retrieveResult, nil
}

// ---------------------------------------------------------------------------
// Mock: core.BatchStorer
// ---------------------------------------------------------------------------

type mockBatchStorer struct {
	// CreateBatch
	createBatchID  int64
	createBatchErr error
	createBatchLog []BatchCreateInput

	// CreateBatchRequests
	createReqsErr error
	createReqsLog []struct {
		batchID int64
		inputs  []BatchCreateRequestInput
	}

	// GetBatch
	getBatchRecord *BatchRecord
	getBatchErr    error

	// UpdateBatchStatus
	updateStatusLog []struct {
		batchID int64
		update  BatchStatusUpdateInput
	}
	updateStatusErr error

	// UpdateBatchProviderID
	updateProviderIDLog []struct {
		batchID         int64
		providerBatchID string
	}
	updateProviderIDErr error

	// GetUnprocessedRequests
	unprocessedReqs []BatchRequestRecord
	unprocessedErr  error

	// MarkRequestProcessed
	markProcessedLog []struct {
		reqID    int64
		respJSON *string
		errMsg   *string
	}
	markProcessedErr error

	// ListBatches
	listBatchesResult []BatchRecord
	listBatchesErr    error

	// CountUnfinishedBatchesByMetadata
	countUnfinishedResult int
	countUnfinishedErr    error
	countUnfinishedCalls  []struct {
		jobType, metaKey, metaValue string
	}
}

func (m *mockBatchStorer) CreateBatch(ctx context.Context, input BatchCreateInput) (int64, error) {
	m.createBatchLog = append(m.createBatchLog, input)
	return m.createBatchID, m.createBatchErr
}

func (m *mockBatchStorer) CreateBatchRequests(ctx context.Context, batchID int64, inputs []BatchCreateRequestInput) error {
	m.createReqsLog = append(m.createReqsLog, struct {
		batchID int64
		inputs  []BatchCreateRequestInput
	}{batchID, inputs})
	return m.createReqsErr
}

func (m *mockBatchStorer) GetBatch(ctx context.Context, batchID int64) (*BatchRecord, error) {
	if m.getBatchErr != nil {
		return nil, m.getBatchErr
	}
	return m.getBatchRecord, nil
}

func (m *mockBatchStorer) UpdateBatchStatus(ctx context.Context, batchID int64, update BatchStatusUpdateInput) error {
	m.updateStatusLog = append(m.updateStatusLog, struct {
		batchID int64
		update  BatchStatusUpdateInput
	}{batchID, update})
	return m.updateStatusErr
}

func (m *mockBatchStorer) UpdateBatchProviderID(ctx context.Context, batchID int64, providerBatchID string) error {
	m.updateProviderIDLog = append(m.updateProviderIDLog, struct {
		batchID         int64
		providerBatchID string
	}{batchID, providerBatchID})
	return m.updateProviderIDErr
}

func (m *mockBatchStorer) GetUnprocessedRequests(ctx context.Context, batchID int64) ([]BatchRequestRecord, error) {
	return m.unprocessedReqs, m.unprocessedErr
}

func (m *mockBatchStorer) MarkRequestProcessed(ctx context.Context, reqID int64, responseJSON *string, errMsg *string) error {
	m.markProcessedLog = append(m.markProcessedLog, struct {
		reqID    int64
		respJSON *string
		errMsg   *string
	}{reqID, responseJSON, errMsg})
	return m.markProcessedErr
}

func (m *mockBatchStorer) ListBatches(ctx context.Context, stateFilter string) ([]BatchRecord, error) {
	return m.listBatchesResult, m.listBatchesErr
}

func (m *mockBatchStorer) CountUnfinishedBatchesByMetadata(ctx context.Context, jobType, metaKey, metaValue string) (int, error) {
	m.countUnfinishedCalls = append(m.countUnfinishedCalls, struct {
		jobType, metaKey, metaValue string
	}{jobType, metaKey, metaValue})
	return m.countUnfinishedResult, m.countUnfinishedErr
}

// ---------------------------------------------------------------------------
// Mock: core.EmbeddingGenerator
// ---------------------------------------------------------------------------

type mockEmbeddingGenerator struct {
	embeddings [][]float32
	err        error
	calls      [][]string
}

func (m *mockEmbeddingGenerator) GenerateEmbeddings(ctx context.Context, _ embedding.Purpose, texts []string) ([][]float32, error) {
	m.calls = append(m.calls, texts)
	if m.err != nil {
		return nil, m.err
	}
	if m.embeddings != nil {
		return m.embeddings, nil
	}
	// Default: return a dummy embedding per text
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = []float32{0.1, 0.2, 0.3}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Mock: core.Storer (minimal, for FactInserter)
// ---------------------------------------------------------------------------

type mockStorer struct {
	nextFactID     int64                // Auto-incrementing fact ID
	similarFacts   []SimilarFact        // What FindSimilarActiveFacts returns
	verbsByFactID  map[int64]string     // GetFactCanonicalVerb lookup
	insertedRelations []FactRelationInput // Track InsertFactRelationsBatch calls
}

func (m *mockStorer) FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error) {
	ids := make([]int64, len(names))
	for i := range names {
		ids[i] = int64(i + 1)
	}
	return ids, nil
}

func (m *mockStorer) FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error) {
	return 1, nil
}

func (m *mockStorer) FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, limit int) ([]SimilarFact, error) {
	return m.similarFacts, nil
}

func (m *mockStorer) InsertFact(ctx context.Context, input StoreFactInput) (int64, error) {
	m.nextFactID++
	return m.nextFactID, nil
}

func (m *mockStorer) DeleteFact(ctx context.Context, factID int64) error {
	return nil
}

func (m *mockStorer) InsertFactRelationsBatch(ctx context.Context, inputs []FactRelationInput) error {
	m.insertedRelations = append(m.insertedRelations, inputs...)
	return nil
}

func (m *mockStorer) IsAncestorOf(ctx context.Context, ancestorID int64, descendantID int64) (bool, error) {
	return false, nil
}

func (m *mockStorer) LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (int64, error) {
	return 1, nil
}

func (m *mockStorer) LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error {
	return nil
}

func (m *mockStorer) GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error) {
	if m.verbsByFactID != nil {
		if v, ok := m.verbsByFactID[factID]; ok {
			return v, nil
		}
	}
	return "CLAIMED", nil
}

// ---------------------------------------------------------------------------
// Helper: mock FactParser
// ---------------------------------------------------------------------------

func mockFactParser(jsonData []byte, sourceURL string, publishedAt *time.Time) ([]backend.Fact, error) {
	return []backend.Fact{
		{
			SubjectEntity:      "Test Person",
			NormalizedFactText: "Test Person claimed something",
			EmbeddingText:      "CLAIMED something",
			CanonicalVerb:      "CLAIMED",
			SourceURL:          sourceURL,
			PublishedAt:        publishedAt,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Helper: create a FactInserter with minimal mock storer
// ---------------------------------------------------------------------------

func newTestFactInserter() *FactInserter {
	return newTestFactInserterWithStorer(&mockStorer{})
}

func newTestFactInserterWithStorer(s Storer) *FactInserter {
	return &FactInserter{
		storer:              s,
		stanceProcessor:     &noopStanceProcessor{},
		autoDedupeThreshold: 0.85,
	}
}

// noopStanceProcessor satisfies StanceProcessor for tests that don't care about
// stance routing (batch-job tests). It returns a fake NEW_STANCE result without
// touching storage.
type noopStanceProcessor struct{}

func (noopStanceProcessor) ProcessNewFact(ctx context.Context, personID int64, factID int64, _ string, _ []float32, _ string) (*StanceProcessResult, error) {
	return &StanceProcessResult{Verdict: "NEW_STANCE", StanceID: factID}, nil
}

// ===========================================================================
// Tests
// ===========================================================================

func TestPrepareAndSubmit(t *testing.T) {
	t.Run("single small item", func(t *testing.T) {
		provider := &mockBatchProvider{name: "test-provider"}
		storer := &mockBatchStorer{createBatchID: 42}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		items := []BatchContentItem{
			{
				SourceURL:  "https://example.com/article1",
				Content:    "Some article content about politics.",
				TargetName: "Marine Le Pen",
				TargetType: "person",
			},
		}

		batchID, err := orch.PrepareAndSubmit(context.Background(), items, map[string]any{"source": "test"})
		if err != nil {
			t.Fatalf("PrepareAndSubmit returned error: %v", err)
		}
		if batchID != 42 {
			t.Fatalf("expected batchID=42, got %d", batchID)
		}

		// Provider should have been called once with 1 request
		if len(provider.submitCalls) != 1 {
			t.Fatalf("expected 1 submit call, got %d", len(provider.submitCalls))
		}
		if len(provider.submitCalls[0]) != 1 {
			t.Fatalf("expected 1 batch request, got %d", len(provider.submitCalls[0]))
		}

		// DB batch should have been created with "pending" provider ID before submit
		if len(storer.createBatchLog) != 1 {
			t.Fatalf("expected 1 CreateBatch call, got %d", len(storer.createBatchLog))
		}
		if storer.createBatchLog[0].Provider != "test-provider" {
			t.Errorf("expected provider 'test-provider', got %q", storer.createBatchLog[0].Provider)
		}
		if storer.createBatchLog[0].ProviderBatchID != "pending" {
			t.Errorf("expected initial provider batch ID 'pending', got %q", storer.createBatchLog[0].ProviderBatchID)
		}
		if storer.createBatchLog[0].JobType != "fact_extraction" {
			t.Errorf("expected job type 'fact_extraction', got %q", storer.createBatchLog[0].JobType)
		}

		// Provider batch ID should have been updated after submit
		if len(storer.updateProviderIDLog) != 1 {
			t.Fatalf("expected 1 UpdateBatchProviderID call, got %d", len(storer.updateProviderIDLog))
		}
		if storer.updateProviderIDLog[0].batchID != 42 {
			t.Errorf("expected batchID=42 in provider ID update, got %d", storer.updateProviderIDLog[0].batchID)
		}
		if storer.updateProviderIDLog[0].providerBatchID != "provider-batch-123" {
			t.Errorf("expected provider batch ID 'provider-batch-123', got %q", storer.updateProviderIDLog[0].providerBatchID)
		}

		// DB requests should have been created
		if len(storer.createReqsLog) != 1 {
			t.Fatalf("expected 1 CreateBatchRequests call, got %d", len(storer.createReqsLog))
		}
		if storer.createReqsLog[0].batchID != 42 {
			t.Errorf("expected batchID=42 in request creation, got %d", storer.createReqsLog[0].batchID)
		}
		reqs := storer.createReqsLog[0].inputs
		if len(reqs) != 1 {
			t.Fatalf("expected 1 DB request, got %d", len(reqs))
		}
		if reqs[0].TargetName != "Marine Le Pen" {
			t.Errorf("expected target 'Marine Le Pen', got %q", reqs[0].TargetName)
		}
		if reqs[0].ChunkIndex != 0 {
			t.Errorf("expected chunk index 0, got %d", reqs[0].ChunkIndex)
		}
	})

	t.Run("multiple items", func(t *testing.T) {
		provider := &mockBatchProvider{name: "openai"}
		storer := &mockBatchStorer{createBatchID: 10}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		items := []BatchContentItem{
			{SourceURL: "https://a.com", Content: "Content A", TargetName: "Person A"},
			{SourceURL: "https://b.com", Content: "Content B", TargetName: "Person B"},
			{SourceURL: "https://c.com", Content: "Content C", TargetName: "Person C"},
		}

		_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
		if err != nil {
			t.Fatalf("PrepareAndSubmit returned error: %v", err)
		}

		if len(provider.submitCalls[0]) != 3 {
			t.Fatalf("expected 3 batch requests, got %d", len(provider.submitCalls[0]))
		}
		if len(storer.createReqsLog[0].inputs) != 3 {
			t.Fatalf("expected 3 DB requests, got %d", len(storer.createReqsLog[0].inputs))
		}
	})

	t.Run("chunking large content", func(t *testing.T) {
		provider := &mockBatchProvider{name: "openai"}
		storer := &mockBatchStorer{createBatchID: 5}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		// Create content well above chunkingThreshold so chunking is forced.
		// Use distinct paragraphs so ChunkText can split properly.
		var paragraphs []string
		for i := 0; i < 30; i++ {
			paragraphs = append(paragraphs, strings.Repeat(fmt.Sprintf("Paragraph %d content. ", i), 60))
		}
		bigContent := strings.Join(paragraphs, "\n\n")

		items := []BatchContentItem{
			{
				SourceURL:  "https://example.com/long-article",
				Content:    bigContent,
				TargetName: "Test Person",
			},
		}

		_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
		if err != nil {
			t.Fatalf("PrepareAndSubmit returned error: %v", err)
		}

		// Should have produced multiple requests due to chunking
		numRequests := len(provider.submitCalls[0])
		if numRequests <= 1 {
			t.Fatalf("expected >1 requests from chunking, got %d (content len: %d, threshold: %d)", numRequests, len(bigContent), chunkingThreshold)
		}

		// DB requests should match provider requests
		dbReqs := storer.createReqsLog[0].inputs
		if len(dbReqs) != numRequests {
			t.Fatalf("expected %d DB requests, got %d", numRequests, len(dbReqs))
		}

		// Chunk indices should be sequential starting from 0
		for i, req := range dbReqs {
			if req.ChunkIndex != i {
				t.Errorf("request %d: expected chunk index %d, got %d", i, i, req.ChunkIndex)
			}
		}

		// All DB requests should share the same source URL
		for i, req := range dbReqs {
			if req.SourceURL == nil || *req.SourceURL != "https://example.com/long-article" {
				t.Errorf("request %d: expected source URL 'https://example.com/long-article'", i)
			}
		}
	})

	t.Run("empty content items skipped", func(t *testing.T) {
		provider := &mockBatchProvider{name: "openai"}
		storer := &mockBatchStorer{createBatchID: 1}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		items := []BatchContentItem{
			{SourceURL: "https://empty.com", Content: "", TargetName: "Person"},
			{SourceURL: "https://whitespace.com", Content: "   \n  ", TargetName: "Person"},
			{SourceURL: "https://valid.com", Content: "Valid content here.", TargetName: "Person"},
		}

		_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
		if err != nil {
			t.Fatalf("PrepareAndSubmit returned error: %v", err)
		}

		// Only the valid item should produce a request
		if len(provider.submitCalls[0]) != 1 {
			t.Fatalf("expected 1 batch request (empty ones skipped), got %d", len(provider.submitCalls[0]))
		}
	})

	t.Run("all empty items returns error", func(t *testing.T) {
		provider := &mockBatchProvider{name: "openai"}
		storer := &mockBatchStorer{createBatchID: 1}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		items := []BatchContentItem{
			{SourceURL: "https://empty.com", Content: "", TargetName: "Person"},
		}

		_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
		if err == nil {
			t.Fatal("expected error for all-empty items, got nil")
		}
		if !strings.Contains(err.Error(), "no valid requests") {
			t.Errorf("expected 'no valid requests' error, got: %v", err)
		}
	})

	t.Run("no items returns error", func(t *testing.T) {
		orch := NewBatchJobOrchestrator(nil, nil, nil, nil, nil)
		_, err := orch.PrepareAndSubmit(context.Background(), nil, nil)
		if err == nil {
			t.Fatal("expected error for nil items, got nil")
		}
		if !strings.Contains(err.Error(), "no content items") {
			t.Errorf("expected 'no content items' error, got: %v", err)
		}
	})

	t.Run("default target type is person", func(t *testing.T) {
		provider := &mockBatchProvider{name: "openai"}
		storer := &mockBatchStorer{createBatchID: 1}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		items := []BatchContentItem{
			{SourceURL: "https://example.com", Content: "Some content", TargetName: "Person", TargetType: ""},
		}

		_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
		if err != nil {
			t.Fatalf("PrepareAndSubmit returned error: %v", err)
		}

		dbReqs := storer.createReqsLog[0].inputs
		if dbReqs[0].TargetType != "person" {
			t.Errorf("expected default target type 'person', got %q", dbReqs[0].TargetType)
		}
	})
}

func TestPrepareAndSubmit_DeduplicatesByCustomID(t *testing.T) {
	// When the same URL+target appears multiple times (e.g., from overlapping content collections),
	// the resulting batch requests should be deduplicated by customID.
	provider := &mockBatchProvider{name: "test"}
	storer := &mockBatchStorer{createBatchID: 1}

	orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

	// Two items with identical URL+target produce the same customID
	items := []BatchContentItem{
		{SourceURL: "https://example.com/article", Content: "Content A", TargetName: "Person A"},
		{SourceURL: "https://example.com/article", Content: "Content A copy", TargetName: "Person A"},
	}

	_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
	if err != nil {
		t.Fatalf("PrepareAndSubmit returned error: %v", err)
	}

	// Provider should receive only 1 request (deduped)
	if len(provider.submitCalls) != 1 {
		t.Fatalf("expected 1 submit call, got %d", len(provider.submitCalls))
	}
	if len(provider.submitCalls[0]) != 1 {
		t.Fatalf("expected 1 batch request after dedup, got %d", len(provider.submitCalls[0]))
	}

	// DB should also receive only 1 request
	if len(storer.createReqsLog) != 1 {
		t.Fatalf("expected 1 CreateBatchRequests call, got %d", len(storer.createReqsLog))
	}
	if len(storer.createReqsLog[0].inputs) != 1 {
		t.Fatalf("expected 1 DB request after dedup, got %d", len(storer.createReqsLog[0].inputs))
	}
}

func TestPrepareAndSubmit_DifferentTargetsSameURL_NotDeduped(t *testing.T) {
	// Same URL but different targets should produce different customIDs and NOT be deduped.
	provider := &mockBatchProvider{name: "test"}
	storer := &mockBatchStorer{createBatchID: 1}

	orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

	items := []BatchContentItem{
		{SourceURL: "https://example.com/article", Content: "Content", TargetName: "Person A"},
		{SourceURL: "https://example.com/article", Content: "Content", TargetName: "Person B"},
	}

	_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
	if err != nil {
		t.Fatalf("PrepareAndSubmit returned error: %v", err)
	}

	if len(provider.submitCalls[0]) != 2 {
		t.Fatalf("expected 2 batch requests (different targets), got %d", len(provider.submitCalls[0]))
	}
}

func TestPrepareAndSubmit_SubmitFailureMarksBatchFailed(t *testing.T) {
	// When provider.Submit fails, the batch should already be persisted in DB and marked as "failed".
	provider := &mockBatchProvider{
		name:      "test",
		submitErr: fmt.Errorf("provider API error"),
	}
	storer := &mockBatchStorer{createBatchID: 7}

	orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

	items := []BatchContentItem{
		{SourceURL: "https://example.com/article", Content: "Some content", TargetName: "Person"},
	}

	_, err := orch.PrepareAndSubmit(context.Background(), items, nil)
	if err == nil {
		t.Fatal("expected error from PrepareAndSubmit, got nil")
	}
	if !strings.Contains(err.Error(), "provider submit failed") {
		t.Errorf("expected 'provider submit failed' error, got: %v", err)
	}

	// Batch should have been created in DB (persist-before-submit)
	if len(storer.createBatchLog) != 1 {
		t.Fatalf("expected 1 CreateBatch call, got %d", len(storer.createBatchLog))
	}
	if storer.createBatchLog[0].ProviderBatchID != "pending" {
		t.Errorf("expected provider batch ID 'pending', got %q", storer.createBatchLog[0].ProviderBatchID)
	}

	// Batch requests should have been created in DB
	if len(storer.createReqsLog) != 1 {
		t.Fatalf("expected 1 CreateBatchRequests call, got %d", len(storer.createReqsLog))
	}

	// Batch should have been marked as failed via UpdateBatchStatus
	if len(storer.updateStatusLog) != 1 {
		t.Fatalf("expected 1 UpdateBatchStatus call (marking failed), got %d", len(storer.updateStatusLog))
	}
	update := storer.updateStatusLog[0]
	if update.batchID != 7 {
		t.Errorf("expected batchID=7 in failed update, got %d", update.batchID)
	}
	if update.update.State != "failed" {
		t.Errorf("expected state 'failed', got %q", update.update.State)
	}
	if update.update.ErrorMessage == nil || !strings.Contains(*update.update.ErrorMessage, "provider API error") {
		t.Errorf("expected error message containing 'provider API error', got %v", update.update.ErrorMessage)
	}

	// Provider ID should NOT have been updated (submit failed)
	if len(storer.updateProviderIDLog) != 0 {
		t.Errorf("expected 0 UpdateBatchProviderID calls, got %d", len(storer.updateProviderIDLog))
	}
}

func TestSubmitContradictionBatch_SubmitFailureMarksBatchFailed(t *testing.T) {
	provider := &mockBatchProvider{
		name:      "test",
		submitErr: fmt.Errorf("contradiction provider error"),
	}
	storer := &mockBatchStorer{createBatchID: 15}

	orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

	requests := []llm.BatchRequest{
		{CustomID: "c:1:10:20", Messages: []llm.ChatMessage{{Role: "user", Content: "test"}}},
	}

	_, err := orch.SubmitContradictionBatch(context.Background(), requests, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Batch should have been persisted before submit attempt
	if len(storer.createBatchLog) != 1 {
		t.Fatalf("expected 1 CreateBatch call, got %d", len(storer.createBatchLog))
	}
	if storer.createBatchLog[0].ProviderBatchID != "pending" {
		t.Errorf("expected 'pending' provider batch ID, got %q", storer.createBatchLog[0].ProviderBatchID)
	}

	// Should be marked as failed
	if len(storer.updateStatusLog) != 1 {
		t.Fatalf("expected 1 UpdateBatchStatus call, got %d", len(storer.updateStatusLog))
	}
	if storer.updateStatusLog[0].update.State != "failed" {
		t.Errorf("expected state 'failed', got %q", storer.updateStatusLog[0].update.State)
	}
}

func TestSubmitContradictionBatch_CapsLargeBatches(t *testing.T) {
	provider := &mockBatchProvider{name: "test"}
	storer := &mockBatchStorer{createBatchID: 100}

	orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

	// Create MaxRequestsPerContradictionBatch + 50 requests
	numRequests := MaxRequestsPerContradictionBatch + 50
	requests := make([]llm.BatchRequest, numRequests)
	for i := range requests {
		requests[i] = llm.BatchRequest{
			CustomID: fmt.Sprintf("c:1:%d:%d", i, i+1000),
			Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
		}
	}

	_, err := orch.SubmitContradictionBatch(context.Background(), requests, map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created exactly 1 DB batch (capped, not split)
	if len(storer.createBatchLog) != 1 {
		t.Fatalf("expected 1 CreateBatch call, got %d", len(storer.createBatchLog))
	}
	if storer.createBatchLog[0].RequestCount != MaxRequestsPerContradictionBatch {
		t.Errorf("expected %d requests, got %d", MaxRequestsPerContradictionBatch, storer.createBatchLog[0].RequestCount)
	}

	// Metadata should record total_requests for chain re-trigger
	totalRaw := storer.createBatchLog[0].Metadata["total_requests"]
	if total, ok := totalRaw.(int); !ok || total != numRequests {
		t.Errorf("expected total_requests=%d in metadata, got %v", numRequests, totalRaw)
	}

	// Should have submitted exactly 1 provider batch
	if len(provider.submitCalls) != 1 {
		t.Fatalf("expected 1 Submit call, got %d", len(provider.submitCalls))
	}
	if len(provider.submitCalls[0]) != MaxRequestsPerContradictionBatch {
		t.Errorf("expected %d requests in submit, got %d", MaxRequestsPerContradictionBatch, len(provider.submitCalls[0]))
	}
}

func TestCheckStatus(t *testing.T) {
	t.Run("completed batch", func(t *testing.T) {
		provider := &mockBatchProvider{
			name: "openai",
			statusResult: &llm.BatchStatus{
				ProviderBatchID: "pb-123",
				State:           llm.BatchStateCompleted,
				CompletedCount:  10,
				FailedCount:     0,
			},
		}
		storer := &mockBatchStorer{
			getBatchRecord: &BatchRecord{
				ID:              1,
				ProviderBatchID: "pb-123",
				State:           "in_progress",
			},
		}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		status, err := orch.CheckStatus(context.Background(), 1)
		if err != nil {
			t.Fatalf("CheckStatus returned error: %v", err)
		}
		if status.State != llm.BatchStateCompleted {
			t.Errorf("expected completed state, got %s", status.State)
		}
		if status.CompletedCount != 10 {
			t.Errorf("expected 10 completed, got %d", status.CompletedCount)
		}

		// Verify DB was updated
		if len(storer.updateStatusLog) != 1 {
			t.Fatalf("expected 1 UpdateBatchStatus call, got %d", len(storer.updateStatusLog))
		}
		update := storer.updateStatusLog[0]
		if update.batchID != 1 {
			t.Errorf("expected batchID=1 in update, got %d", update.batchID)
		}
		if update.update.State != "completed" {
			t.Errorf("expected state 'completed' in update, got %q", update.update.State)
		}
		if update.update.CompletedCount == nil || *update.update.CompletedCount != 10 {
			t.Errorf("expected completed count 10 in update")
		}
		if update.update.CompletedAt == nil {
			t.Error("expected completed_at to be set for completed state")
		}
	})

	t.Run("in progress batch", func(t *testing.T) {
		provider := &mockBatchProvider{
			name: "openai",
			statusResult: &llm.BatchStatus{
				ProviderBatchID: "pb-456",
				State:           llm.BatchStateInProgress,
				CompletedCount:  3,
				FailedCount:     0,
			},
		}
		storer := &mockBatchStorer{
			getBatchRecord: &BatchRecord{
				ID:              2,
				ProviderBatchID: "pb-456",
				State:           "pending",
			},
		}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		status, err := orch.CheckStatus(context.Background(), 2)
		if err != nil {
			t.Fatalf("CheckStatus returned error: %v", err)
		}
		if status.State != llm.BatchStateInProgress {
			t.Errorf("expected in_progress state, got %s", status.State)
		}

		// CompletedAt should NOT be set for in_progress
		update := storer.updateStatusLog[0].update
		if update.CompletedAt != nil {
			t.Error("expected completed_at to be nil for in_progress state")
		}
	})

	t.Run("failed batch sets completed_at", func(t *testing.T) {
		provider := &mockBatchProvider{
			name: "openai",
			statusResult: &llm.BatchStatus{
				ProviderBatchID: "pb-789",
				State:           llm.BatchStateFailed,
				ErrorMessage:    "rate limit exceeded",
				FailedCount:     5,
			},
		}
		storer := &mockBatchStorer{
			getBatchRecord: &BatchRecord{
				ID:              3,
				ProviderBatchID: "pb-789",
				State:           "in_progress",
			},
		}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		status, err := orch.CheckStatus(context.Background(), 3)
		if err != nil {
			t.Fatalf("CheckStatus returned error: %v", err)
		}
		if status.State != llm.BatchStateFailed {
			t.Errorf("expected failed state, got %s", status.State)
		}

		update := storer.updateStatusLog[0].update
		if update.CompletedAt == nil {
			t.Error("expected completed_at to be set for failed state")
		}
		if update.ErrorMessage == nil || *update.ErrorMessage != "rate limit exceeded" {
			t.Error("expected error message to be set")
		}
		if update.FailedCount == nil || *update.FailedCount != 5 {
			t.Error("expected failed count to be 5")
		}
	})

	t.Run("provider error propagated", func(t *testing.T) {
		provider := &mockBatchProvider{
			name:      "openai",
			statusErr: fmt.Errorf("network timeout"),
		}
		storer := &mockBatchStorer{
			getBatchRecord: &BatchRecord{
				ID:              1,
				ProviderBatchID: "pb-000",
			},
		}

		orch := NewBatchJobOrchestrator(provider, storer, nil, nil, nil)

		_, err := orch.CheckStatus(context.Background(), 1)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "network timeout") {
			t.Errorf("expected 'network timeout' in error, got: %v", err)
		}
	})
}

func TestLoadBatchContentFromDir(t *testing.T) {
	t.Run("loads valid JSON files", func(t *testing.T) {
		dir := t.TempDir()

		// Write valid content files
		pubDate := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		file1 := map[string]any{
			"url":            "https://example.com/article1",
			"content":        "Article one content about politics.",
			"date_published": pubDate.Format(time.RFC3339),
		}
		file2 := map[string]any{
			"url":     "https://example.com/article2",
			"content": "Article two content about economics.",
		}

		writeJSON(t, filepath.Join(dir, "article1.json"), file1)
		writeJSON(t, filepath.Join(dir, "article2.json"), file2)

		items, err := LoadBatchContentFromDir(dir, "Test Person", "person")
		if err != nil {
			t.Fatalf("LoadBatchContentFromDir returned error: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}

		// Verify items have correct fields
		for _, item := range items {
			if item.TargetName != "Test Person" {
				t.Errorf("expected target 'Test Person', got %q", item.TargetName)
			}
			if item.TargetType != "person" {
				t.Errorf("expected target type 'person', got %q", item.TargetType)
			}
			if item.Content == "" {
				t.Error("expected non-empty content")
			}
			if item.SourceURL == "" {
				t.Error("expected non-empty source URL")
			}
		}
	})

	t.Run("skips empty content files", func(t *testing.T) {
		dir := t.TempDir()

		validFile := map[string]any{
			"url":     "https://example.com/valid",
			"content": "Valid content here.",
		}
		emptyFile := map[string]any{
			"url":     "https://example.com/empty",
			"content": "",
		}
		whitespaceFile := map[string]any{
			"url":     "https://example.com/ws",
			"content": "   \n  ",
		}

		writeJSON(t, filepath.Join(dir, "valid.json"), validFile)
		writeJSON(t, filepath.Join(dir, "empty.json"), emptyFile)
		writeJSON(t, filepath.Join(dir, "whitespace.json"), whitespaceFile)

		items, err := LoadBatchContentFromDir(dir, "Test Person", "person")
		if err != nil {
			t.Fatalf("LoadBatchContentFromDir returned error: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 item (empty/whitespace skipped), got %d", len(items))
		}
		if items[0].SourceURL != "https://example.com/valid" {
			t.Errorf("expected valid URL, got %q", items[0].SourceURL)
		}
	})

	t.Run("uses target-specific subdirectory", func(t *testing.T) {
		dir := t.TempDir()

		// Create a target-specific subdirectory matching the slug
		subDir := filepath.Join(dir, "test_person")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}

		file := map[string]any{
			"url":     "https://example.com/sub",
			"content": "Content in subdirectory.",
		}
		writeJSON(t, filepath.Join(subDir, "article.json"), file)

		items, err := LoadBatchContentFromDir(dir, "Test Person", "person")
		if err != nil {
			t.Fatalf("LoadBatchContentFromDir returned error: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 item from subdirectory, got %d", len(items))
		}
	})

	t.Run("error on empty directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := LoadBatchContentFromDir(dir, "Test Person", "person")
		if err == nil {
			t.Fatal("expected error for empty directory, got nil")
		}
		if !strings.Contains(err.Error(), "no JSON files") {
			t.Errorf("expected 'no JSON files' error, got: %v", err)
		}
	})

	t.Run("default target type is person", func(t *testing.T) {
		dir := t.TempDir()

		file := map[string]any{
			"url":     "https://example.com/article",
			"content": "Some content.",
		}
		writeJSON(t, filepath.Join(dir, "article.json"), file)

		items, err := LoadBatchContentFromDir(dir, "Test Person", "")
		if err != nil {
			t.Fatalf("LoadBatchContentFromDir returned error: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(items))
		}
		if items[0].TargetType != "person" {
			t.Errorf("expected default target type 'person', got %q", items[0].TargetType)
		}
	})

	t.Run("skips invalid JSON files", func(t *testing.T) {
		dir := t.TempDir()

		// Write invalid JSON
		if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not json {{{"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Write valid JSON
		valid := map[string]any{
			"url":     "https://example.com/good",
			"content": "Good content.",
		}
		writeJSON(t, filepath.Join(dir, "good.json"), valid)

		items, err := LoadBatchContentFromDir(dir, "Test Person", "person")
		if err != nil {
			t.Fatalf("LoadBatchContentFromDir returned error: %v", err)
		}
		// Only the valid file should be loaded
		if len(items) != 1 {
			t.Fatalf("expected 1 item (invalid JSON skipped), got %d", len(items))
		}
	})
}

func TestShortHash(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		h1 := shortHash("https://example.com/article")
		h2 := shortHash("https://example.com/article")
		if h1 != h2 {
			t.Errorf("shortHash is not deterministic: %q != %q", h1, h2)
		}
	})

	t.Run("different inputs produce different hashes", func(t *testing.T) {
		h1 := shortHash("https://example.com/a")
		h2 := shortHash("https://example.com/b")
		if h1 == h2 {
			t.Errorf("different inputs produced the same hash: %q", h1)
		}
	})

	t.Run("returns 12 hex characters", func(t *testing.T) {
		h := shortHash("test-input")
		if len(h) != 12 {
			t.Errorf("expected 12 character hash, got %d characters: %q", len(h), h)
		}
		// Verify it's valid hex
		for _, c := range h {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("non-hex character in hash: %c (hash: %q)", c, h)
				break
			}
		}
	})

	t.Run("empty string", func(t *testing.T) {
		h := shortHash("")
		if h == "" {
			t.Error("shortHash of empty string should not be empty")
		}
		if len(h) != 12 {
			t.Errorf("expected 12 character hash, got %d", len(h))
		}
	})
}

// ===========================================================================
// Tests: Batch Chain (fact_extraction → classification → contradiction)
// ===========================================================================

func TestChain_FactExtractionTriggersClassification(t *testing.T) {
	// Simulate: a fact_extraction batch completes, the poller retrieves it,
	// and the chain should auto-submit a classification batch.

	ctx := context.Background()

	// Mock storer with similar facts for classification
	factStorer := &mockStorer{
		similarFacts: []SimilarFact{
			{ID: 100, Text: "Existing fact about topic X", Distance: 0.15},
			{ID: 200, Text: "Another existing fact about topic X", Distance: 0.25},
		},
		verbsByFactID: map[int64]string{
			100: "CLAIMED",
			200: "DENIED",
		},
	}
	inserter := newTestFactInserterWithStorer(factStorer)

	provider := &mockBatchProvider{name: "test"}
	batchStorer := &mockBatchStorer{
		createBatchID: 99, // ID assigned to new classification batch
		getBatchRecord: &BatchRecord{
			ID:              1,
			ProviderBatchID: "provider-batch-1",
			State:           "completed",
			JobType:         "fact_extraction",
			Metadata:        map[string]any{},
		},
	}

	embedder := &mockEmbeddingGenerator{}

	orch := NewBatchJobOrchestrator(provider, batchStorer, embedder, inserter, mockFactParser)

	// Simulate: 2 facts were inserted from this batch
	insertedFacts := []InsertedFactInfo{
		{
			FactID:    1,
			Text:      "New fact A about topic X",
			Verb:      "CLAIMED",
			Embedding: []float32{0.1, 0.2, 0.3},
			PersonIDs: []int64{10},
		},
		{
			FactID:    2,
			Text:      "New fact B about topic Y",
			Verb:      "DENIED",
			Embedding: []float32{0.4, 0.5, 0.6},
			PersonIDs: []int64{10},
		},
	}

	// Call SubmitClassificationBatch directly
	batchID, err := orch.SubmitClassificationBatch(ctx, insertedFacts, 1)
	if err != nil {
		t.Fatalf("SubmitClassificationBatch failed: %v", err)
	}
	if batchID != 99 {
		t.Errorf("expected batch ID 99, got %d", batchID)
	}

	// Verify: provider received batch requests
	if len(provider.submitCalls) != 1 {
		t.Fatalf("expected 1 provider submit call, got %d", len(provider.submitCalls))
	}
	requests := provider.submitCalls[0]
	if len(requests) != 2 {
		t.Fatalf("expected 2 classification requests (one per fact), got %d", len(requests))
	}

	// Verify custom IDs encode fact IDs
	if requests[0].CustomID != "cl:1" {
		t.Errorf("expected custom ID 'cl:1', got %q", requests[0].CustomID)
	}
	if requests[1].CustomID != "cl:2" {
		t.Errorf("expected custom ID 'cl:2', got %q", requests[1].CustomID)
	}

	// Verify: batch was created with correct job type and metadata
	if len(batchStorer.createBatchLog) != 1 {
		t.Fatalf("expected 1 CreateBatch call, got %d", len(batchStorer.createBatchLog))
	}
	created := batchStorer.createBatchLog[0]
	if created.JobType != "classification" {
		t.Errorf("expected job type 'classification', got %q", created.JobType)
	}
	parentID, ok := created.Metadata["parent_extraction_batch_id"]
	if !ok {
		t.Fatal("expected parent_extraction_batch_id in metadata")
	}
	if parentID != int64(1) {
		t.Errorf("expected parent_extraction_batch_id=1, got %v", parentID)
	}

	// Verify: prompts contain the classification prompt structure
	for _, req := range requests {
		if len(req.Messages) == 0 {
			t.Fatal("expected messages in request")
		}
		prompt := req.Messages[0].Content
		if !strings.Contains(prompt, "NEW FACT:") {
			t.Errorf("prompt missing 'NEW FACT:' section")
		}
		if !strings.Contains(prompt, "EXISTING FACTS:") {
			t.Errorf("prompt missing 'EXISTING FACTS:' section")
		}
		if !strings.Contains(prompt, "duplicate") {
			t.Errorf("prompt missing 'duplicate' classification option")
		}
	}
}

func TestChain_ClassificationResultsWriteRelations(t *testing.T) {
	// Simulate: a classification batch completes and the LLM identified a duplicate.

	ctx := context.Background()

	factStorer := &mockStorer{}
	inserter := newTestFactInserterWithStorer(factStorer)

	provider := &mockBatchProvider{name: "test"}
	batchStorer := &mockBatchStorer{
		getBatchRecord: &BatchRecord{
			ID:              5,
			ProviderBatchID: "provider-cls-batch",
			State:           "completed",
			JobType:         "classification",
			Metadata:        map[string]any{"parent_extraction_batch_id": float64(1)},
		},
		unprocessedReqs: []BatchRequestRecord{
			{
				ID:       10,
				BatchID:  5,
				CustomID: "cl:42", // fact ID 42
			},
		},
	}

	// LLM response: fact 42 is a duplicate of fact 100
	classificationJSON := `{"classifications": [{"existing_fact_id": 100, "relation": "duplicate", "reason": "Same claim from different source"}]}`
	provider.retrieveResult = []llm.BatchResponseItem{
		{
			CustomID: "cl:42",
			Response: &llm.ChatResponse{
				Choices: []llm.ChatChoice{
					{Message: llm.ChatMessage{Content: classificationJSON}},
				},
			},
		},
	}

	orch := NewBatchJobOrchestrator(provider, batchStorer, nil, inserter, nil)

	result, err := orch.RetrieveAndProcess(ctx, 5)
	if err != nil {
		t.Fatalf("RetrieveAndProcess failed: %v", err)
	}

	if result.Processed != 1 {
		t.Errorf("expected 1 processed, got %d", result.Processed)
	}

	// Verify: fact_relations were written
	if len(factStorer.insertedRelations) != 1 {
		t.Fatalf("expected 1 relation inserted, got %d", len(factStorer.insertedRelations))
	}
	rel := factStorer.insertedRelations[0]
	if rel.GeneralFactID != 100 {
		t.Errorf("expected general fact ID 100 (existing), got %d", rel.GeneralFactID)
	}
	if rel.SpecificFactID != 42 {
		t.Errorf("expected specific fact ID 42 (new), got %d", rel.SpecificFactID)
	}
	if rel.RelationType != "duplicate" {
		t.Errorf("expected relation type 'duplicate', got %q", rel.RelationType)
	}
}

func TestChain_ClassificationCompletionTriggersContradiction(t *testing.T) {
	// Simulate: last classification batch completes (no siblings pending),
	// should trigger contradictionSubmitter.

	ctx := context.Background()

	factStorer := &mockStorer{}
	inserter := newTestFactInserterWithStorer(factStorer)

	batchStorer := &mockBatchStorer{
		countUnfinishedResult: 0, // No pending classification batches
	}

	orch := NewBatchJobOrchestrator(nil, batchStorer, nil, inserter, nil)

	// Track if contradictionSubmitter was called
	contradictionCalled := false
	orch.SetContradictionSubmitter(func(ctx context.Context) error {
		contradictionCalled = true
		return nil
	})

	batch := &BatchRecord{
		ID:       5,
		JobType:  "classification",
		Metadata: map[string]any{"parent_extraction_batch_id": float64(1)},
	}

	orch.maybeSubmitContradictionBatch(ctx, batch)

	if !contradictionCalled {
		t.Error("expected contradictionSubmitter to be called when no pending classification batches")
	}

	// Verify the right metadata was queried
	if len(batchStorer.countUnfinishedCalls) != 1 {
		t.Fatalf("expected 1 CountUnfinished call, got %d", len(batchStorer.countUnfinishedCalls))
	}
	call := batchStorer.countUnfinishedCalls[0]
	if call.jobType != "classification" {
		t.Errorf("expected jobType 'classification', got %q", call.jobType)
	}
	if call.metaKey != "parent_extraction_batch_id" {
		t.Errorf("expected metaKey 'parent_extraction_batch_id', got %q", call.metaKey)
	}
}

func TestChain_ClassificationDeferredWhenSiblingsPending(t *testing.T) {
	// Simulate: classification batch completes but siblings are still pending.
	// Should NOT trigger contradictionSubmitter.

	ctx := context.Background()

	batchStorer := &mockBatchStorer{
		countUnfinishedResult: 2, // 2 sibling classification batches still running
	}

	orch := NewBatchJobOrchestrator(nil, batchStorer, nil, newTestFactInserter(), nil)

	contradictionCalled := false
	orch.SetContradictionSubmitter(func(ctx context.Context) error {
		contradictionCalled = true
		return nil
	})

	batch := &BatchRecord{
		ID:       5,
		JobType:  "classification",
		Metadata: map[string]any{"parent_extraction_batch_id": float64(1)},
	}

	orch.maybeSubmitContradictionBatch(ctx, batch)

	if contradictionCalled {
		t.Error("contradictionSubmitter should NOT be called when siblings are pending")
	}
}

func TestChain_NoClassificationWhenNoSimilarFacts(t *testing.T) {
	// If inserted facts have no similar existing facts, no classification batch
	// should be submitted (returns 0, nil).

	ctx := context.Background()

	factStorer := &mockStorer{
		similarFacts: nil, // No similar facts
	}
	inserter := newTestFactInserterWithStorer(factStorer)

	provider := &mockBatchProvider{name: "test"}
	batchStorer := &mockBatchStorer{createBatchID: 99}

	orch := NewBatchJobOrchestrator(provider, batchStorer, nil, inserter, nil)

	facts := []InsertedFactInfo{
		{FactID: 1, Text: "Unique fact", Verb: "CLAIMED", Embedding: []float32{0.1}, PersonIDs: []int64{10}},
	}

	batchID, err := orch.SubmitClassificationBatch(ctx, facts, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batchID != 0 {
		t.Errorf("expected batch ID 0 (no requests needed), got %d", batchID)
	}

	// Provider should NOT have been called
	if len(provider.submitCalls) != 0 {
		t.Errorf("expected 0 provider calls, got %d", len(provider.submitCalls))
	}
}

func TestParseClassificationResponse(t *testing.T) {
	t.Run("duplicate and unrelated", func(t *testing.T) {
		json := `{"classifications": [
			{"existing_fact_id": 100, "relation": "duplicate", "reason": "Same claim"},
			{"existing_fact_id": 200, "relation": "unrelated", "reason": "Different topic"},
			{"existing_fact_id": 300, "relation": "implication_new_is_specific", "reason": "More specific"}
		]}`

		results, err := ParseClassificationResponse(json)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// "unrelated" should be filtered out
		if len(results) != 2 {
			t.Fatalf("expected 2 results (unrelated filtered), got %d", len(results))
		}
		if results[0].ExistingFactID != 100 || results[0].Relation != "duplicate" {
			t.Errorf("unexpected first result: %+v", results[0])
		}
		if results[1].ExistingFactID != 300 || results[1].Relation != "implication_new_is_specific" {
			t.Errorf("unexpected second result: %+v", results[1])
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := ParseClassificationResponse("not json")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("empty classifications", func(t *testing.T) {
		results, err := ParseClassificationResponse(`{"classifications": []}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("string fact IDs", func(t *testing.T) {
		json := `{"classifications": [{"existing_fact_id": "42", "relation": "duplicate", "reason": "Same"}]}`
		results, err := ParseClassificationResponse(json)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 || results[0].ExistingFactID != 42 {
			t.Errorf("expected fact ID 42, got %+v", results)
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(t *testing.T, path string, data any) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("failed to marshal JSON for %s: %v", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
