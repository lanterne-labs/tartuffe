-- +goose Up
-- Add event_log table for durable event streaming
-- This table stores events that can be streamed to clients for realtime updates
-- Clients can resume from a specific event ID for robust reconnect handling

CREATE TABLE IF NOT EXISTS event_log (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    topic TEXT NOT NULL, -- e.g. 'person_updated', 'contradiction_added'
    entity_type TEXT NOT NULL, -- e.g. 'person', 'contradiction'
    entity_id BIGINT NOT NULL, -- e.g. person_id or contradiction_id
    payload JSONB NOT NULL DEFAULT '{}' -- Additional event data
);

-- Index for efficient querying by entity_type and entity_id
CREATE INDEX IF NOT EXISTS idx_event_log_entity ON event_log(entity_type, entity_id);

-- Index for efficient querying by topic
CREATE INDEX IF NOT EXISTS idx_event_log_topic ON event_log(topic);

-- Index for efficient querying by created_at for chronological ordering
CREATE INDEX IF NOT EXISTS idx_event_log_created_at ON event_log(created_at);