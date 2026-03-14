package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanBatchTables truncates batch-related tables before each test.
func cleanBatchTables(t *testing.T, storer *PostgresStorer) {
	t.Helper()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE llm_batch_requests, llm_batches RESTART IDENTITY CASCADE")
	require.NoError(t, err, "Failed to truncate batch tables")
}

func TestCreateAndGetBatch(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	input := CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_abc123",
		JobType:         "fact_extraction",
		RequestCount:    5,
		Metadata:        map[string]any{"target": "Marine Le Pen", "source": "news"},
	}

	id, err := storer.CreateBatch(ctx, input)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	batch, err := storer.GetBatch(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, batch)

	assert.Equal(t, id, batch.ID)
	assert.Equal(t, "openai", batch.Provider)
	assert.Equal(t, "batch_abc123", batch.ProviderBatchID)
	assert.Equal(t, "pending", batch.State) // default state from DB
	assert.Equal(t, "fact_extraction", batch.JobType)
	assert.Equal(t, 5, batch.RequestCount)
	assert.Equal(t, 0, batch.CompletedCount)
	assert.Equal(t, 0, batch.FailedCount)
	assert.Nil(t, batch.ErrorMessage)
	assert.False(t, batch.SubmittedAt.IsZero())
	assert.Nil(t, batch.CompletedAt)
	assert.Nil(t, batch.RetrievedAt)
	assert.Equal(t, "Marine Le Pen", batch.Metadata["target"])
	assert.Equal(t, "news", batch.Metadata["source"])
}

func TestGetBatchByProviderID(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	input := CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_provider_xyz",
		JobType:         "contradiction_detection",
		RequestCount:    3,
		Metadata:        map[string]any{"version": float64(2)},
	}

	id, err := storer.CreateBatch(ctx, input)
	require.NoError(t, err)

	batch, err := storer.GetBatchByProviderID(ctx, "batch_provider_xyz")
	require.NoError(t, err)
	require.NotNil(t, batch)

	assert.Equal(t, id, batch.ID)
	assert.Equal(t, "openai", batch.Provider)
	assert.Equal(t, "batch_provider_xyz", batch.ProviderBatchID)
	assert.Equal(t, "contradiction_detection", batch.JobType)
	assert.Equal(t, 3, batch.RequestCount)
	assert.Equal(t, float64(2), batch.Metadata["version"])

	// Non-existent provider ID should return an error.
	_, err = storer.GetBatchByProviderID(ctx, "nonexistent_batch_id")
	assert.Error(t, err)
}

func TestCreateBatchRequests(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	batchID, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_req_test",
		JobType:         "fact_extraction",
		RequestCount:    3,
	})
	require.NoError(t, err)

	sourceURL := "https://example.com/article"
	pubTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	requests := []CreateBatchRequestInput{
		{
			CustomID:    "req-001",
			SourceURL:   &sourceURL,
			TargetName:  "Marine Le Pen",
			TargetType:  "person",
			ChunkIndex:  0,
			PublishedAt: &pubTime,
		},
		{
			CustomID:   "req-002",
			SourceURL:  nil,
			TargetName: "Marine Le Pen",
			TargetType: "person",
			ChunkIndex: 1,
		},
		{
			CustomID:    "req-003",
			SourceURL:   &sourceURL,
			TargetName:  "Emmanuel Macron",
			TargetType:  "person",
			ChunkIndex:  0,
			PublishedAt: &pubTime,
		},
	}

	err = storer.CreateBatchRequests(ctx, batchID, requests)
	require.NoError(t, err)

	// All should be unprocessed.
	unprocessed, err := storer.GetUnprocessedRequests(ctx, batchID)
	require.NoError(t, err)
	require.Len(t, unprocessed, 3)

	// Verify ordering by id (insertion order).
	assert.Equal(t, "req-001", unprocessed[0].CustomID)
	assert.Equal(t, "req-002", unprocessed[1].CustomID)
	assert.Equal(t, "req-003", unprocessed[2].CustomID)

	// Verify fields on first request.
	r := unprocessed[0]
	assert.Equal(t, batchID, r.BatchID)
	assert.NotNil(t, r.SourceURL)
	assert.Equal(t, "https://example.com/article", *r.SourceURL)
	assert.Equal(t, "Marine Le Pen", r.TargetName)
	assert.Equal(t, "person", r.TargetType)
	assert.Equal(t, 0, r.ChunkIndex)
	assert.NotNil(t, r.PublishedAt)
	assert.Equal(t, pubTime.UTC(), r.PublishedAt.UTC())
	assert.Nil(t, r.ResponseJSON)
	assert.Nil(t, r.ErrorMessage)
	assert.False(t, r.Processed)

	// Verify nil source_url on second request.
	assert.Nil(t, unprocessed[1].SourceURL)
	assert.Nil(t, unprocessed[1].PublishedAt)

	// Empty inputs should be a no-op.
	err = storer.CreateBatchRequests(ctx, batchID, []CreateBatchRequestInput{})
	require.NoError(t, err)
}

func TestMarkRequestProcessed(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	batchID, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_mark_test",
		JobType:         "fact_extraction",
		RequestCount:    2,
	})
	require.NoError(t, err)

	requests := []CreateBatchRequestInput{
		{CustomID: "mark-001", TargetName: "Person A", TargetType: "person", ChunkIndex: 0},
		{CustomID: "mark-002", TargetName: "Person B", TargetType: "person", ChunkIndex: 0},
	}
	err = storer.CreateBatchRequests(ctx, batchID, requests)
	require.NoError(t, err)

	// Get all unprocessed to learn their IDs.
	unprocessed, err := storer.GetUnprocessedRequests(ctx, batchID)
	require.NoError(t, err)
	require.Len(t, unprocessed, 2)

	// Mark the first one as processed with a response.
	responseJSON := `{"facts": [{"text": "some fact"}]}`
	err = storer.MarkRequestProcessed(ctx, unprocessed[0].ID, &responseJSON, nil)
	require.NoError(t, err)

	// Now only the second should remain unprocessed.
	remaining, err := storer.GetUnprocessedRequests(ctx, batchID)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "mark-002", remaining[0].CustomID)
	assert.False(t, remaining[0].Processed)

	// Mark the second one as processed with an error.
	errMsg := "rate limit exceeded"
	err = storer.MarkRequestProcessed(ctx, unprocessed[1].ID, nil, &errMsg)
	require.NoError(t, err)

	// No unprocessed requests should remain.
	remaining, err = storer.GetUnprocessedRequests(ctx, batchID)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

func TestUpdateBatchStatus(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	batchID, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_status_test",
		JobType:         "fact_extraction",
		RequestCount:    10,
	})
	require.NoError(t, err)

	// Update state and counts.
	completed := 7
	failed := 1
	now := time.Now().UTC().Truncate(time.Microsecond)
	err = storer.UpdateBatchStatus(ctx, batchID, BatchStatusUpdate{
		State:          "completed",
		CompletedCount: &completed,
		FailedCount:    &failed,
		CompletedAt:    &now,
	})
	require.NoError(t, err)

	// Verify the update persists.
	batch, err := storer.GetBatch(ctx, batchID)
	require.NoError(t, err)

	assert.Equal(t, "completed", batch.State)
	assert.Equal(t, 7, batch.CompletedCount)
	assert.Equal(t, 1, batch.FailedCount)
	assert.NotNil(t, batch.CompletedAt)
	assert.Equal(t, now, batch.CompletedAt.UTC().Truncate(time.Microsecond))
	assert.Nil(t, batch.RetrievedAt)

	// Update retrieved_at and error message in a second update.
	retrievedAt := now.Add(5 * time.Minute)
	errMsg := "partial failure"
	err = storer.UpdateBatchStatus(ctx, batchID, BatchStatusUpdate{
		RetrievedAt:  &retrievedAt,
		ErrorMessage: &errMsg,
	})
	require.NoError(t, err)

	batch, err = storer.GetBatch(ctx, batchID)
	require.NoError(t, err)

	// State should remain "completed" since we passed an empty string (COALESCE keeps old value).
	assert.Equal(t, "completed", batch.State)
	require.NotNil(t, batch.ErrorMessage)
	assert.Equal(t, "partial failure", *batch.ErrorMessage)
	assert.NotNil(t, batch.RetrievedAt)
	assert.Equal(t, retrievedAt, batch.RetrievedAt.UTC().Truncate(time.Microsecond))
}

func TestListBatches(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	cleanBatchTables(t, storer)

	ctx := context.Background()

	// Create three batches with different states.
	id1, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_list_1",
		JobType:         "fact_extraction",
		RequestCount:    5,
	})
	require.NoError(t, err)

	id2, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "openai",
		ProviderBatchID: "batch_list_2",
		JobType:         "fact_extraction",
		RequestCount:    3,
	})
	require.NoError(t, err)

	id3, err := storer.CreateBatch(ctx, CreateBatchInput{
		Provider:        "anthropic",
		ProviderBatchID: "batch_list_3",
		JobType:         "contradiction_detection",
		RequestCount:    8,
	})
	require.NoError(t, err)

	// Update batch 1 to "completed".
	err = storer.UpdateBatchStatus(ctx, id1, BatchStatusUpdate{State: "completed"})
	require.NoError(t, err)

	// Update batch 2 to "failed".
	err = storer.UpdateBatchStatus(ctx, id2, BatchStatusUpdate{State: "failed"})
	require.NoError(t, err)

	// Batch 3 remains "pending" (default).
	_ = id3

	// List all batches (no filter).
	all, err := storer.ListBatches(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Results are ordered by submitted_at DESC, so the most recently created comes first.
	// Since all were created near-simultaneously, we verify all provider_batch_ids are present.
	providerIDs := make(map[string]bool)
	for _, b := range all {
		providerIDs[b.ProviderBatchID] = true
	}
	assert.True(t, providerIDs["batch_list_1"])
	assert.True(t, providerIDs["batch_list_2"])
	assert.True(t, providerIDs["batch_list_3"])

	// Filter by "completed" state.
	completed, err := storer.ListBatches(ctx, "completed")
	require.NoError(t, err)
	require.Len(t, completed, 1)
	assert.Equal(t, "batch_list_1", completed[0].ProviderBatchID)
	assert.Equal(t, "completed", completed[0].State)

	// Filter by "failed" state.
	failed, err := storer.ListBatches(ctx, "failed")
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, "batch_list_2", failed[0].ProviderBatchID)

	// Filter by "pending" state (default).
	pending, err := storer.ListBatches(ctx, "pending")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "batch_list_3", pending[0].ProviderBatchID)

	// Filter by non-existent state returns empty.
	none, err := storer.ListBatches(ctx, "cancelled")
	require.NoError(t, err)
	assert.Empty(t, none)
}
