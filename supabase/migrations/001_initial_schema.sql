-- +goose Up
-- Enable pgvector extension (run once per database)
CREATE EXTENSION IF NOT EXISTS vector;

-- Table to store unique, normalized facts
CREATE TABLE facts (
    -- Unique identifier for the fact
    id BIGSERIAL PRIMARY KEY,
    -- The normalized, core factual statement (unique)
    fact_text TEXT NOT NULL UNIQUE,
    -- Vector embedding of the fact_text (dimension from Mistral model, e.g., 1024)
    embedding vector(1024) NOT NULL,
    -- Optional: Relevant topics or categories associated with the fact
    topics TEXT[],
    -- Timestamp of when the fact was first created
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    -- Timestamp of when the fact was last updated (e.g., embedding refined)
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Add an index for efficient vector similarity search (HNSW is often recommended)
-- Adjust ef_construction and m based on your data size and performance needs
CREATE INDEX ON facts USING hnsw (embedding vector_l2_ops);
-- Or use IVFFlat: CREATE INDEX ON facts USING ivfflat (embedding vector_l2_ops) WITH (lists = 100);

-- Trigger to automatically update updated_at timestamp
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER set_facts_timestamp
BEFORE UPDATE ON facts
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();


-- Table to store unique persons mentioned
CREATE TABLE persons (
    -- Unique identifier for the person
    id BIGSERIAL PRIMARY KEY,
    -- Normalized name of the person (unique)
    name TEXT NOT NULL UNIQUE,
    -- Timestamp of when the person was first added
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);


-- Table to link facts to their specific sources (URLs) and original quotes
CREATE TABLE fact_sources (
    -- Unique identifier for this specific source instance
    id BIGSERIAL PRIMARY KEY,
    -- Foreign key referencing the normalized fact
    fact_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    -- The URL where the fact was found
    source_url TEXT NOT NULL,
    -- The original, non-normalized quote or text snippet from the source
    original_quote TEXT,
    -- Publication date of the source, if available
    published_at TIMESTAMP WITH TIME ZONE,
    -- Timestamp of when this source was scraped/processed
    scraped_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    -- Ensure a source URL is linked only once to a specific fact
    UNIQUE (fact_id, source_url)
);

-- Index for faster lookups by URL (useful for pre-checking)
CREATE INDEX idx_fact_sources_url ON fact_sources(source_url);
-- Index for faster lookups by fact_id
CREATE INDEX idx_fact_sources_fact_id ON fact_sources(fact_id);


-- Junction table to link persons mentioned in a specific source quote
CREATE TABLE fact_mentions (
    -- Unique identifier for the mention link
    id BIGSERIAL PRIMARY KEY,
    -- Foreign key referencing the specific source instance
    fact_source_id BIGINT NOT NULL REFERENCES fact_sources(id) ON DELETE CASCADE,
    -- Foreign key referencing the person mentioned
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    -- Ensure a person is linked only once to a specific fact source
    UNIQUE (fact_source_id, person_id)
);

-- Indexes for faster lookups/joins
CREATE INDEX idx_fact_mentions_fact_source_id ON fact_mentions(fact_source_id);
CREATE INDEX idx_fact_mentions_person_id ON fact_mentions(person_id); 