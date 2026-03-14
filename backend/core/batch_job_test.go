package core

import (
	"context"
	"digital-mirror/backend"
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

// ---------------------------------------------------------------------------
// Mock: core.EmbeddingGenerator
// ---------------------------------------------------------------------------

type mockEmbeddingGenerator struct {
	embeddings [][]float32
	err        error
	calls      [][]string
}

func (m *mockEmbeddingGenerator) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
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

type mockStorer struct{}

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
	return nil, nil
}

func (m *mockStorer) InsertFact(ctx context.Context, input StoreFactInput) (int64, error) {
	return 1, nil
}

func (m *mockStorer) InsertFactRelationsBatch(ctx context.Context, inputs []FactRelationInput) error {
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
	return &FactInserter{
		storer:              &mockStorer{},
		classifier:          nil,
		similarityThreshold: 0.40,
		autoDedupeThreshold: 0.85,
	}
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

		// DB batch should have been created
		if len(storer.createBatchLog) != 1 {
			t.Fatalf("expected 1 CreateBatch call, got %d", len(storer.createBatchLog))
		}
		if storer.createBatchLog[0].Provider != "test-provider" {
			t.Errorf("expected provider 'test-provider', got %q", storer.createBatchLog[0].Provider)
		}
		if storer.createBatchLog[0].JobType != "fact_extraction" {
			t.Errorf("expected job type 'fact_extraction', got %q", storer.createBatchLog[0].JobType)
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

		// Create content larger than chunkingThreshold (25000 chars)
		// Use distinct paragraphs so ChunkText can split properly
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
