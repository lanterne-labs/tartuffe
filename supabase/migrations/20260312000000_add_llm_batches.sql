-- +goose Up
CREATE TABLE llm_batches (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    provider_batch_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    job_type TEXT NOT NULL DEFAULT 'fact_extraction',
    request_count INT NOT NULL DEFAULT 0,
    completed_count INT DEFAULT 0,
    failed_count INT DEFAULT 0,
    error_message TEXT,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    retrieved_at TIMESTAMPTZ,
    metadata JSONB DEFAULT '{}'
);

CREATE TABLE llm_batch_requests (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES llm_batches(id) ON DELETE CASCADE,
    custom_id TEXT NOT NULL,
    source_url TEXT,
    target_name TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT 'person',
    chunk_index INT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,
    response_json TEXT,
    error_message TEXT,
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (batch_id, custom_id)
);

CREATE INDEX idx_llm_batches_state ON llm_batches(state);
CREATE INDEX idx_llm_batch_requests_batch_id ON llm_batch_requests(batch_id);
CREATE INDEX idx_llm_batch_requests_processed ON llm_batch_requests(batch_id, processed);
