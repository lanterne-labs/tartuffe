package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// LLMBatch represents a batch job stored in the database.
type LLMBatch struct {
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

// LLMBatchRequest represents a single request within a batch.
type LLMBatchRequest struct {
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

// CreateBatchInput holds the parameters for creating a new batch.
type CreateBatchInput struct {
	Provider        string
	ProviderBatchID string
	JobType         string
	RequestCount    int
	Metadata        map[string]any
}

// CreateBatchRequestInput holds the parameters for creating a batch request.
type CreateBatchRequestInput struct {
	CustomID    string
	SourceURL   *string
	TargetName  string
	TargetType  string
	ChunkIndex  int
	PublishedAt *time.Time
}

// BatchStatusUpdate holds the fields to update on a batch.
type BatchStatusUpdate struct {
	State          string
	CompletedCount *int
	FailedCount    *int
	ErrorMessage   *string
	CompletedAt    *time.Time
	RetrievedAt    *time.Time
}

// CreateBatch inserts a new batch record and returns its ID.
func (s *PostgresStorer) CreateBatch(ctx context.Context, input CreateBatchInput) (int64, error) {
	metadataJSON, err := json.Marshal(input.Metadata)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO llm_batches (provider, provider_batch_id, job_type, request_count, metadata)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		input.Provider, input.ProviderBatchID, input.JobType, input.RequestCount, metadataJSON,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to insert batch: %w", err)
	}

	log.Debug().Int64("batch_id", id).Str("provider", input.Provider).Int("requests", input.RequestCount).Msg("Created batch")
	return id, nil
}

// CreateBatchRequests bulk-inserts request records for a batch.
func (s *PostgresStorer) CreateBatchRequests(ctx context.Context, batchID int64, inputs []CreateBatchRequestInput) error {
	if len(inputs) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, inp := range inputs {
		batch.Queue(
			`INSERT INTO llm_batch_requests (batch_id, custom_id, source_url, target_name, target_type, chunk_index, published_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			batchID, inp.CustomID, inp.SourceURL, inp.TargetName, inp.TargetType, inp.ChunkIndex, inp.PublishedAt,
		)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := 0; i < len(inputs); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to insert batch request %d: %w", i, err)
		}
	}

	log.Debug().Int64("batch_id", batchID).Int("count", len(inputs)).Msg("Created batch requests")
	return nil
}

// GetBatch retrieves a batch by its database ID.
func (s *PostgresStorer) GetBatch(ctx context.Context, batchID int64) (*LLMBatch, error) {
	b := &LLMBatch{}
	var metadataJSON []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, provider, provider_batch_id, state, job_type, request_count,
		        completed_count, failed_count, error_message, submitted_at, completed_at, retrieved_at, metadata
		 FROM llm_batches WHERE id = $1`, batchID,
	).Scan(&b.ID, &b.Provider, &b.ProviderBatchID, &b.State, &b.JobType, &b.RequestCount,
		&b.CompletedCount, &b.FailedCount, &b.ErrorMessage, &b.SubmittedAt, &b.CompletedAt, &b.RetrievedAt, &metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch %d: %w", batchID, err)
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &b.Metadata)
	}
	return b, nil
}

// GetBatchByProviderID retrieves a batch by its provider-assigned ID.
func (s *PostgresStorer) GetBatchByProviderID(ctx context.Context, providerBatchID string) (*LLMBatch, error) {
	b := &LLMBatch{}
	var metadataJSON []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, provider, provider_batch_id, state, job_type, request_count,
		        completed_count, failed_count, error_message, submitted_at, completed_at, retrieved_at, metadata
		 FROM llm_batches WHERE provider_batch_id = $1`, providerBatchID,
	).Scan(&b.ID, &b.Provider, &b.ProviderBatchID, &b.State, &b.JobType, &b.RequestCount,
		&b.CompletedCount, &b.FailedCount, &b.ErrorMessage, &b.SubmittedAt, &b.CompletedAt, &b.RetrievedAt, &metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch by provider_id %s: %w", providerBatchID, err)
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &b.Metadata)
	}
	return b, nil
}

// UpdateBatchStatus updates the state and counts of a batch.
func (s *PostgresStorer) UpdateBatchStatus(ctx context.Context, batchID int64, update BatchStatusUpdate) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE llm_batches SET
			state = COALESCE(NULLIF($2, ''), state),
			completed_count = COALESCE($3, completed_count),
			failed_count = COALESCE($4, failed_count),
			error_message = COALESCE($5, error_message),
			completed_at = COALESCE($6, completed_at),
			retrieved_at = COALESCE($7, retrieved_at)
		 WHERE id = $1`,
		batchID, update.State, update.CompletedCount, update.FailedCount,
		update.ErrorMessage, update.CompletedAt, update.RetrievedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to update batch %d: %w", batchID, err)
	}
	return nil
}

// GetUnprocessedRequests retrieves all unprocessed requests for a batch.
func (s *PostgresStorer) GetUnprocessedRequests(ctx context.Context, batchID int64) ([]LLMBatchRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, batch_id, custom_id, source_url, target_name, target_type, chunk_index, published_at,
		        response_json, error_message, processed
		 FROM llm_batch_requests
		 WHERE batch_id = $1 AND processed = FALSE
		 ORDER BY id`, batchID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query unprocessed requests: %w", err)
	}
	defer rows.Close()

	var requests []LLMBatchRequest
	for rows.Next() {
		var r LLMBatchRequest
		if err := rows.Scan(&r.ID, &r.BatchID, &r.CustomID, &r.SourceURL, &r.TargetName, &r.TargetType,
			&r.ChunkIndex, &r.PublishedAt, &r.ResponseJSON, &r.ErrorMessage, &r.Processed); err != nil {
			return nil, fmt.Errorf("failed to scan batch request: %w", err)
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

// MarkRequestProcessed updates a request with its response and marks it as processed.
func (s *PostgresStorer) MarkRequestProcessed(ctx context.Context, reqID int64, responseJSON *string, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE llm_batch_requests
		 SET response_json = $2, error_message = $3, processed = TRUE
		 WHERE id = $1`,
		reqID, responseJSON, errMsg,
	)
	if err != nil {
		return fmt.Errorf("failed to mark request %d as processed: %w", reqID, err)
	}
	return nil
}

// ListBatches returns batches, optionally filtered by state.
func (s *PostgresStorer) ListBatches(ctx context.Context, stateFilter string) ([]LLMBatch, error) {
	var query string
	var args []any

	if stateFilter != "" {
		query = `SELECT id, provider, provider_batch_id, state, job_type, request_count,
		                completed_count, failed_count, error_message, submitted_at, completed_at, retrieved_at, metadata
		         FROM llm_batches WHERE state = $1 ORDER BY submitted_at DESC`
		args = append(args, stateFilter)
	} else {
		query = `SELECT id, provider, provider_batch_id, state, job_type, request_count,
		                completed_count, failed_count, error_message, submitted_at, completed_at, retrieved_at, metadata
		         FROM llm_batches ORDER BY submitted_at DESC`
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list batches: %w", err)
	}
	defer rows.Close()

	var batches []LLMBatch
	for rows.Next() {
		var b LLMBatch
		var metadataJSON []byte
		if err := rows.Scan(&b.ID, &b.Provider, &b.ProviderBatchID, &b.State, &b.JobType, &b.RequestCount,
			&b.CompletedCount, &b.FailedCount, &b.ErrorMessage, &b.SubmittedAt, &b.CompletedAt, &b.RetrievedAt, &metadataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan batch: %w", err)
		}
		if metadataJSON != nil {
			_ = json.Unmarshal(metadataJSON, &b.Metadata)
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// CountUnfinishedBatchesByMetadata counts batches of a given job type that are not yet
// fully processed, filtered by a metadata key-value pair. Used for batch chaining:
// "are there any pending classification batches for this parent extraction batch?"
func (s *PostgresStorer) CountUnfinishedBatchesByMetadata(ctx context.Context, jobType, metaKey, metaValue string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM llm_batches
		 WHERE job_type = $1
		   AND metadata->>$2 = $3
		   AND (state IN ('pending', 'in_progress') OR (state = 'completed' AND retrieved_at IS NULL))`,
		jobType, metaKey, metaValue,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count unfinished batches: %w", err)
	}
	return count, nil
}
