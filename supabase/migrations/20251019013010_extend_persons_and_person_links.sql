-- +goose Up
-- Extend core persons with entity_type and attributes; add person_links table
-- Enable pgcrypto for gen_random_uuid (safe if already enabled)
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- persons extensions (idempotent)
ALTER TABLE persons
  ADD COLUMN IF NOT EXISTS entity_type text CHECK (entity_type IN ('human','party','company','organization')) DEFAULT 'human';

ALTER TABLE persons
  ADD COLUMN IF NOT EXISTS attributes jsonb NOT NULL DEFAULT '{}';

-- Link humans to parties (or other affiliations)
CREATE TABLE IF NOT EXISTS person_links (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  src_person_id bigint NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
  dst_person_id bigint NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
  link_type text NOT NULL CHECK (link_type IN ('membership','affiliation')),
  valid_from date,
  valid_to date,
  attributes jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_person_links_src ON person_links(src_person_id);
CREATE INDEX IF NOT EXISTS idx_person_links_dst ON person_links(dst_person_id);

