-- +goose Up

CREATE TABLE IF NOT EXISTS stance_documents (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    cluster_label TEXT NOT NULL,
    cluster_embedding vector(1536) NOT NULL,
    summary TEXT NOT NULL,
    key_fact_ids BIGINT[] NOT NULL DEFAULT '{}',
    fact_count INT NOT NULL DEFAULT 1,
    last_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (person_id, cluster_label)
);

CREATE TABLE IF NOT EXISTS stance_document_facts (
    stance_document_id BIGINT NOT NULL REFERENCES stance_documents(id) ON DELETE CASCADE,
    fact_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stance_document_id, fact_id)
);

CREATE INDEX IF NOT EXISTS idx_stance_documents_person_id ON stance_documents(person_id);
CREATE INDEX IF NOT EXISTS idx_stance_document_facts_fact_id ON stance_document_facts(fact_id);
