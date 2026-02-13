-- +goose Up
-- supabase/migrations/002_add_contradictions_table.sql

CREATE TABLE contradictions (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    fact_a_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    fact_b_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    reason TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    detected_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP, -- Renamed from updated_at for clarity
    CONSTRAINT uq_contradiction_link UNIQUE (person_id, fact_a_id, fact_b_id),
    CONSTRAINT chk_fact_order CHECK (fact_a_id < fact_b_id) -- Ensures order for unique constraint
);

-- Trigger to update detected_at timestamp on update (e.g., if reason changes)
-- Assuming trigger_set_timestamp() function already exists from 001_initial_schema.sql
CREATE TRIGGER set_contradictions_detected_at_timestamp
BEFORE UPDATE ON contradictions
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();

-- Optional: Indexes for faster lookups
CREATE INDEX idx_contradictions_person_id ON contradictions(person_id);
CREATE INDEX idx_contradictions_fact_a_id ON contradictions(fact_a_id);
CREATE INDEX idx_contradictions_fact_b_id ON contradictions(fact_b_id); 